package backup

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type logicalTestRecord struct {
	ID        int64  `gorm:"primaryKey"`
	Secret    string `json:"-"`
	Name      string `gorm:"size:4"`
	Empty     string
	Nullable  string
	Enabled   bool  `gorm:"default:true"`
	Count     int64 `gorm:"default:7"`
	Huge      int64
	Ratio     float64
	Payload   []byte
	CreatedAt time.Time
	Runtime   string `gorm:"-"`
}

type logicalUnsupportedRecord struct {
	ID      int64
	Special string `gorm:"type:jsonb"`
}

type logicalEvolutionV1 struct {
	ID       int64 `gorm:"primaryKey"`
	Existing string
}

func (logicalEvolutionV1) TableName() string { return "logical_evolution" }

type logicalEvolutionV2 struct {
	ID       int64 `gorm:"primaryKey"`
	Existing string
	Added    string `json:"-"`
}

func (logicalEvolutionV2) TableName() string { return "logical_evolution" }

func TestLogicalTablesCoverPersistentFieldsAndExplicitExclusions(t *testing.T) {
	testDB := setupBackupTest(t)
	models.AllTables = []any{
		models.User{}, models.ApiKey{}, models.UserSession{}, models.UploadSession{},
		models.EmbyObservedEvidenceIndex{}, models.EmbyItemMembership{}, logicalTestRecord{},
	}
	tables, excluded, err := logicalTables(testDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 4 || len(excluded) != 3 {
		t.Fatalf("tables=%d exclusions=%d", len(tables), len(excluded))
	}
	want := map[string][]string{
		"users":                {"two_factor_secret", "two_factor_pending_secret", "singleton_key"},
		"api_keys":             {"key_hash", "is_active"},
		"upload_sessions":      {"id", "callback"},
		"logical_test_records": {"secret", "nullable", "created_at"},
	}
	for _, table := range tables {
		columns := make(map[string]bool)
		for _, column := range table.Columns {
			columns[column.Name] = true
		}
		for _, name := range want[table.Name] {
			if !columns[name] {
				t.Errorf("table %s lost persistent column %s", table.Name, name)
			}
		}
		if columns["runtime"] {
			t.Error("gorm ignored field exported")
		}
	}
	if _, err := describeLogicalTable(testDB, logicalUnsupportedRecord{}); err == nil {
		t.Error("unsupported column type must fail explicitly")
	}
}

func TestLogicalTablesSupportCurrentRegisteredModels(t *testing.T) {
	registered := models.AllTables
	testDB := setupBackupTest(t)
	models.AllTables = registered
	tables, excluded, err := logicalTables(testDB)
	if err != nil {
		t.Fatal(err)
	}
	excludedTables := 0
	for _, exclusion := range excluded {
		if exclusion.Column == "" {
			excludedTables++
		}
	}
	if len(tables)+excludedTables != len(registered) {
		t.Fatal("registered model missing from manifest or exclusions")
	}
}

func createLogicalTestBackup(t *testing.T) (*gorm.DB, string, *logicalManifest) {
	t.Helper()
	testDB := setupBackupTest(t)
	models.AllTables = []any{logicalTestRecord{}}
	if err := testDB.AutoMigrate(&logicalTestRecord{}); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 7, 12, 34, 56, 123456789, time.FixedZone("source", 8*60*60))
	if err := testDB.Table("logical_test_records").Create(map[string]any{
		"id": int64(9007199254740993), "secret": "private-secret", "name": "中文🙂", "empty": "", "nullable": nil,
		"enabled": false, "count": int64(0), "huge": int64(math.MaxInt64), "ratio": 7.875456789012345,
		"payload": []byte{0, 255, 128}, "created_at": when,
	}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	return testDB, dir, manifest
}

func TestLogicalBackupPreservesDatabaseValues(t *testing.T) {
	_, dir, manifest := createLogicalTestBackup(t)
	if manifest.FormatVersion != logicalFormatVersion || manifest.SchemaVersion != models.MaxVersionCode {
		t.Fatalf("unexpected manifest: %+v", manifest)
	}
	var got map[string]any
	if err := readLogicalRows(dir, manifest.Tables[0], func(row map[string]any) error { got = row; return nil }); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": int64(9007199254740993), "secret": "private-secret", "name": "中文🙂", "empty": "", "nullable": nil,
		"enabled": false, "count": int64(0), "huge": int64(math.MaxInt64), "ratio": 7.875456789012345,
		"payload": []byte{0, 255, 128}, "created_at": time.Date(2026, 10, 7, 4, 34, 56, 123457000, time.UTC),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("logical backup changed a persistent value")
	}
	for _, column := range manifest.Tables[0].Columns {
		if column.Name == "runtime" {
			t.Fatal("runtime field must not enter backup")
		}
	}
}

func TestLogicalBackupArchiveFormat(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprintf("format_%d", version), func(t *testing.T) {
			conn, dir, manifest := createLogicalTestBackup(t)
			if manifest.FormatVersion != 2 || manifest.Tables[0].File != "tables/logicalTestRecord.json" {
				t.Fatal("new backup must use format 2 with nested table files")
			}
			if version != 2 {
				manifest.FormatVersion = version
				content, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			archive := filepath.Join(t.TempDir(), "backup.zip")
			if err := helpers.ZipDir(dir, archive); err != nil {
				t.Fatal(err)
			}
			if err := conn.Exec("UPDATE logical_test_records SET name = ?", "保留").Error; err != nil {
				t.Fatal(err)
			}
			err := Restore(archive)
			wantName := "中文🙂"
			if version != 2 {
				if err == nil || !strings.Contains(err.Error(), "不支持备份格式") {
					t.Fatalf("unknown format must be rejected: %v", err)
				}
				wantName = "保留"
			} else if err != nil {
				t.Fatal(err)
			}
			var got logicalTestRecord
			if err := conn.First(&got).Error; err != nil || got.Name != wantName {
				t.Fatalf("layout restore failed: %v", err)
			}
		})
	}
}

func TestLogicalBackupRejectsUnexpectedTablePaths(t *testing.T) {
	for _, name := range []string{"logicalTestRecord.json", "../logicalTestRecord.json", "/tables/logicalTestRecord.json", `tables\logicalTestRecord.json`, "tables/../logicalTestRecord.json"} {
		t.Run(name, func(t *testing.T) {
			_, dir, manifest := createLogicalTestBackup(t)
			manifest.Tables[0].File = name
			content, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), content, 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "backup.zip")
			if err := helpers.ZipDir(dir, archive); err != nil {
				t.Fatal(err)
			}
			if err := Restore(archive); err == nil || !strings.Contains(err.Error(), "表或列定义") {
				t.Fatalf("unexpected table path must be rejected: %v", err)
			}
		})
	}
}

func TestLogicalBackupApplicationVersion(t *testing.T) {
	previousVersion := helpers.Version
	helpers.Version = "v0.99.0+backup-test"
	t.Cleanup(func() { helpers.Version = previousVersion })
	for _, scenario := range []string{"current", "missing", "different", "invalid_type"} {
		t.Run(scenario, func(t *testing.T) {
			testDB, dir, _ := createLogicalTestBackup(t)
			path := filepath.Join(dir, logicalManifestFile)
			content, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(content, &metadata); err != nil {
				t.Fatal(err)
			}
			if string(metadata["application_version"]) != `"v0.99.0+backup-test"` {
				t.Fatal("备份清单未记录当前应用版本")
			}
			switch scenario {
			case "missing":
				delete(metadata, "application_version")
			case "different":
				metadata["application_version"] = json.RawMessage(`"v0.1.0"`)
			case "invalid_type":
				metadata["application_version"] = json.RawMessage(`42`)
			}
			content, err = json.Marshal(metadata)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, content, 0600); err != nil {
				t.Fatal(err)
			}
			archive := filepath.Join(t.TempDir(), "backup.zip")
			if err := helpers.ZipDir(dir, archive); err != nil {
				t.Fatal(err)
			}
			if err := testDB.Model(&logicalTestRecord{}).
				Where("id = ?", int64(9007199254740993)).UpdateColumn("name", "edit").Error; err != nil {
				t.Fatal(err)
			}
			err = Restore(archive)
			invalid := scenario == "invalid_type"
			if (err != nil) != invalid {
				t.Fatalf("应用版本兼容处理不符合预期：%v", err)
			}
			wantName := "中文🙂"
			if invalid {
				wantName = "edit"
			}
			var got logicalTestRecord
			if err := testDB.First(&got).Error; err != nil || got.Name != wantName {
				t.Fatalf("恢复后的数据不符合预期：name=%q, want=%q, err=%v", got.Name, wantName, err)
			}
		})
	}
}

func TestLogicalBackupLongCredentialsPassPostgresPreflight(t *testing.T) {
	testDB := setupBackupTest(t)
	models.AllTables = []any{models.Account{}}
	if err := testDB.AutoMigrate(&models.Account{}); err != nil {
		t.Fatal(err)
	}
	want := models.Account{
		Name: "long credentials", Token: strings.Repeat("t", 6*1024), RefreshToken: strings.Repeat("r", 8*1024),
		Username: strings.Repeat("用户名", 64), Password: strings.Repeat("p", 512),
		TokenFailedReason: strings.Repeat("upstream unavailable;", 64),
	}
	if err := testDB.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	target := testDB.Session(&gorm.Session{NewDB: true})
	target.Dialector = postgresSequenceFailureDialect{Dialector: target.Dialector}
	manifest, err := prepareLogicalBackup(dir, target)
	if err != nil {
		t.Fatal(err)
	}
	if err := readLogicalRows(dir, manifest.Tables[0], func(row map[string]any) error {
		for name, value := range map[string]string{
			"token": want.Token, "refresh_token": want.RefreshToken, "username": want.Username,
			"password": want.Password, "token_failed_reason": want.TokenFailedReason,
		} {
			if row[name] != value {
				return fmt.Errorf("column %s changed", name)
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalBackupLargeRowIsRestorable(t *testing.T) {
	testDB := setupBackupTest(t)
	want := strings.Repeat("x", 17*1024*1024)
	if err := testDB.Create(&backupTestItem{ID: 1, Name: want}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if err := readLogicalRows(dir, manifest.Tables[0], func(row map[string]any) error {
		if row["name"] != want {
			return errors.New("large row changed")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestLogicalBackupAutomaticallyIncludesRegisteredTablesAndMigratedFields(t *testing.T) {
	testDB := setupBackupTest(t)
	models.AllTables = []any{logicalEvolutionV1{}}
	if err := testDB.AutoMigrate(&logicalEvolutionV1{}); err != nil {
		t.Fatal(err)
	}
	if err := testDB.Create(&logicalEvolutionV1{ID: 1, Existing: "original"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := writeLogicalBackup(testDB, t.TempDir(), nil); err != nil {
		t.Fatal(err)
	}
	models.AllTables = []any{logicalEvolutionV2{}, backupOtherTestItem{}}
	if err := testDB.AutoMigrate(&logicalEvolutionV2{}); err != nil {
		t.Fatal(err)
	}
	if err := testDB.Model(&logicalEvolutionV2{}).Where("id = ?", 1).Update("added", "new secret").Error; err != nil {
		t.Fatal(err)
	}
	if err := testDB.Create(&backupOtherTestItem{ID: 2, Name: "new table"}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Tables) != 2 {
		t.Fatal("new registered table omitted")
	}
	want := map[string]map[string]any{
		"logical_evolution":       {"id": int64(1), "existing": "original", "added": "new secret"},
		"backup_other_test_items": {"id": int64(2), "name": "new table"},
	}
	for _, table := range manifest.Tables {
		if err := readLogicalRows(dir, table, func(row map[string]any) error {
			if !reflect.DeepEqual(row, want[table.Name]) {
				return errors.New("new persistent field or new table value omitted")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestLogicalBackupRejectsUnknownOrMissingSourceColumns(t *testing.T) {
	for _, test := range []struct{ name, statement string }{
		{"extra", `ALTER TABLE backup_test_items ADD COLUMN hidden_legacy TEXT`},
		{"missing", `ALTER TABLE backup_test_items DROP COLUMN name`},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDB := setupBackupTest(t)
			if err := testDB.Exec(test.statement).Error; err != nil {
				t.Fatal(err)
			}
			if err := writeLogicalBackup(testDB, t.TempDir(), nil); err == nil {
				t.Fatal("schema drift must not silently discard columns")
			}
		})
	}
}

func TestLogicalBackupDeclaresRetiredColumnsWithoutChangingSource(t *testing.T) {
	testDB := setupBackupTest(t)
	models.AllTables = []any{models.Migrator{}, models.EmbyMediaItem{}, models.BackupConfig{}, models.Settings{}, models.SyncPath{}}
	for _, model := range models.AllTables {
		if err := testDB.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	retired := map[string][]string{
		"emby_media_items": {"emby_data"},
		"backup_config":    {"maintenance_mode", "maintenance_mode_time"},
		"settings":         {"bai_du_pan_qps", "bai_du_pan_qpm", "bai_du_pan_qph", "bai_du_pan_qpt"},
		"sync_paths":       {"baidu_sync_method"},
	}
	retiredValue := func(column string) any {
		if column == "emby_data" {
			return "retired-value"
		}
		return int64(73)
	}
	for table, columns := range retired {
		if err := testDB.Table(table).Create(map[string]any{"id": 1}).Error; err != nil {
			t.Fatal(err)
		}
		for _, column := range columns {
			dataType := "INTEGER"
			if column == "emby_data" {
				dataType = "TEXT"
			}
			if err := testDB.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + dataType).Error; err != nil {
				t.Fatal(err)
			}
			if err := testDB.Table(table).Where("id = ?", 1).UpdateColumn(column, retiredValue(column)).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	// 实际升级不会 DROP 这些旧列；备份不得因模型已移除它们而拒绝已升级的数据库。
	if err := testDB.Exec("UPDATE migrator SET version_code = 66").Error; err != nil {
		t.Fatal(err)
	}
	if err := models.Migrate(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Excluded) != 8 {
		t.Fatalf("retired exclusions=%d, want 8", len(manifest.Excluded))
	}
	declared := make(map[string]bool)
	for _, exclusion := range manifest.Excluded {
		if exclusion.Column == "" || exclusion.Reason == "" {
			t.Fatal("retired columns must be identified explicitly")
		}
		declared[exclusion.Name+"."+exclusion.Column] = true
	}
	for table, columns := range retired {
		for _, column := range columns {
			if !declared[table+"."+column] {
				t.Fatalf("retired column %s.%s missing from manifest", table, column)
			}
			var actual any
			if err := testDB.Table(table).Select(column).Where("id = ?", 1).Row().Scan(&actual); err != nil || !reflect.DeepEqual(actual, retiredValue(column)) {
				t.Fatalf("backup changed retired column %s.%s: %v", table, column, err)
			}
		}
	}
	for _, table := range manifest.Tables {
		if err := readLogicalRows(dir, table, func(row map[string]any) error {
			for _, column := range retired[table.Name] {
				if _, exists := row[column]; exists {
					return fmt.Errorf("retired column %s still exported as business data", column)
				}
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	// 排除清单描述当前 schema 的统一规则；不要求目标数据库也残留旧列。
	fresh, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := fresh.DB()
	if err != nil {
		t.Fatal(err)
	}
	connection.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = connection.Close() })
	postgresTarget := fresh.Session(&gorm.Session{NewDB: true})
	postgresTarget.Dialector = postgresSequenceFailureDialect{Dialector: fresh.Dialector}
	if _, err := prepareLogicalBackup(dir, postgresTarget); err != nil {
		t.Fatal(err)
	}
	for _, model := range models.AllTables {
		if err := fresh.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	if err := fresh.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	freshDir := t.TempDir()
	if err := writeLogicalBackup(fresh, freshDir, nil); err != nil {
		t.Fatal(err)
	}
	freshManifest, err := prepareLogicalBackup(freshDir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(freshManifest.Excluded, manifest.Excluded) {
		t.Fatal("fresh and upgraded schema must declare the same retirement policy")
	}
	if err := testDB.Exec("ALTER TABLE settings ADD COLUMN unknown_live_data TEXT").Error; err != nil {
		t.Fatal(err)
	}
	if err := writeLogicalBackup(testDB, t.TempDir(), nil); err == nil || !strings.Contains(err.Error(), "unknown_live_data") {
		t.Fatalf("unknown extra column must still fail explicitly: %v", err)
	}
}

func TestLogicalBackupRejectsInvalidTextWithoutReplacingBytes(t *testing.T) {
	for _, test := range []struct{ name, value string }{
		{"invalid_utf8", "private-" + string([]byte{0xff})},
		{"nul", "private-\x00"},
	} {
		t.Run(test.name, func(t *testing.T) {
			testDB := setupBackupTest(t)
			if err := testDB.Exec("INSERT INTO backup_test_items (id, name) VALUES (?, ?)", 1, test.value).Error; err != nil {
				t.Fatal(err)
			}
			err := writeLogicalBackup(testDB, t.TempDir(), nil)
			if err == nil || !strings.Contains(err.Error(), "backup_test_items") || !strings.Contains(err.Error(), "name") || strings.Contains(err.Error(), "private-") {
				t.Fatalf("invalid text must identify table/column without value: %v", err)
			}
		})
	}
}

func TestLogicalBackupRejectsInvalidSchemaVersion(t *testing.T) {
	for _, scenario := range []string{"missing", "empty", "old", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			testDB := setupBackupTest(t)
			switch scenario {
			case "missing":
				if err := testDB.Migrator().DropTable(&models.Migrator{}); err != nil {
					t.Fatal(err)
				}
			case "empty":
				if err := testDB.Exec("DELETE FROM migrator").Error; err != nil {
					t.Fatal(err)
				}
			case "old":
				if err := testDB.Exec("UPDATE migrator SET version_code = ?", models.MaxVersionCode-1).Error; err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				if err := testDB.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := writeLogicalBackup(testDB, t.TempDir(), nil); err == nil {
				t.Fatal("invalid schema version accepted")
			}
		})
	}
}

func TestLogicalBackupAfterVersion60TransferMigration(t *testing.T) {
	registered := models.AllTables
	testDB := setupBackupTest(t)
	models.AllTables = registered
	for _, model := range registered {
		if err := testDB.AutoMigrate(model); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []any{models.SyncFile{}, models.DbDownloadTask{}, models.DbUploadTask{}} {
		if err := testDB.Migrator().DropTable(model); err != nil {
			t.Fatal(err)
		}
	}
	// 复用模型迁移测试中的 60 版传输结构，验证真实升级入口会清除已迁移的旧列。
	for _, statement := range []string{
		`CREATE TABLE sync_files (id integer primary key, source_type text, file_id text, pick_code text, sha1 text, path text, file_name text)`,
		`CREATE TABLE db_download_tasks (id integer primary key, source text, source_type text, sync_file_id integer, remote_file_id text, remote_path text, file_name text)`,
		`CREATE TABLE db_upload_tasks (id integer primary key, source text, source_type text, sync_file_id integer, remote_file_id text, remote_path_id text, file_name text, completed_remote_file_id text, completed_pick_code text)`,
		`INSERT INTO sync_files (id, source_type, file_id, pick_code, sha1, path, file_name) VALUES (1, '115', 'file-id', 'pick-code', 'sha1', '/remote', 'movie.mkv')`,
		`INSERT INTO db_download_tasks (id, source, source_type, sync_file_id, remote_file_id, remote_path, file_name) VALUES (1, 'strm_sync', '115', 1, 'legacy-pick', '/remote', 'movie.mkv')`,
		`INSERT INTO db_upload_tasks (id, source, source_type, sync_file_id, remote_file_id, remote_path_id, file_name, completed_remote_file_id, completed_pick_code) VALUES (1, 'strm_sync', '115', 1, '/upload/movie.mkv', 'parent-id', 'movie.mkv', 'completed-id', 'completed-pick')`,
		`UPDATE migrator SET version_code = 60`,
	} {
		if err := testDB.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := models.Migrate(); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(testDB, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, testDB)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range manifest.Tables {
		if table.Name != "db_upload_tasks" {
			continue
		}
		if err := readLogicalRows(dir, table, func(row map[string]any) error {
			if row["remote_file_id"] != "completed-id" || row["remote_pick_code"] != "completed-pick" {
				return errors.New("migrated remote identity lost in backup")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPrepareLogicalBackupValidatesEntireArchive(t *testing.T) {
	for _, scenario := range []string{"checksum", "count", "missing_file", "missing_column", "duplicate_column", "unknown_column", "version", "column_definition", "exclusion", "malformed_json", "target_length"} {
		t.Run(scenario, func(t *testing.T) {
			testDB, dir, manifest := createLogicalTestBackup(t)
			file := filepath.Join(dir, manifest.Tables[0].File)
			switch scenario {
			case "checksum":
				manifest.Tables[0].SHA256 = strings.Repeat("0", 64)
			case "count":
				manifest.Tables[0].RowCount++
			case "missing_file":
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			case "missing_column":
				if err := os.WriteFile(file, []byte(`{"id":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "duplicate_column":
				if err := os.WriteFile(file, []byte(`{"id":1,"id":2}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown_column":
				if err := os.WriteFile(file, []byte(`{"unknown":1}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "version":
				manifest.SchemaVersion--
			case "column_definition":
				manifest.Tables[0].Columns[0].Type = "text"
			case "exclusion":
				manifest.Excluded = []logicalExclusion{{Name: "users", Reason: "accidental"}}
			case "malformed_json":
				if err := os.WriteFile(file, []byte("{broken"), 0600); err != nil {
					t.Fatal(err)
				}
			case "target_length":
				if err := testDB.Exec("UPDATE logical_test_records SET name = ?", "超过四个字符").Error; err != nil {
					t.Fatal(err)
				}
				if err := writeLogicalBackup(testDB, dir, nil); err != nil {
					t.Fatal(err)
				}
				testDB = testDB.Session(&gorm.Session{NewDB: true})
				testDB.Dialector = postgresSequenceFailureDialect{Dialector: testDB.Dialector}
			}
			if scenario != "target_length" {
				content, err := json.Marshal(manifest)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := prepareLogicalBackup(dir, testDB); err == nil {
				t.Fatal("invalid archive must fail before restore")
			}
		})
	}
}

func TestPrepareLogicalBackupChecksActualMigrationVersion(t *testing.T) {
	for _, scenario := range []string{"different_version", "empty", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			testDB := setupBackupTest(t)
			models.AllTables = []any{models.Migrator{}}
			dir := t.TempDir()
			if err := writeLogicalBackup(testDB, dir, nil); err != nil {
				t.Fatal(err)
			}
			manifest, err := prepareLogicalBackup(dir, testDB)
			if err != nil {
				t.Fatal(err)
			}
			var row map[string]any
			if err := readLogicalRows(dir, manifest.Tables[0], func(value map[string]any) error { row = value; return nil }); err != nil {
				t.Fatal(err)
			}
			row["version_code"] = int64(models.MaxVersionCode - 1)
			content, err := json.Marshal(row)
			if err != nil {
				t.Fatal(err)
			}
			content = append(content, '\n')
			switch scenario {
			case "empty":
				content = nil
				manifest.Tables[0].RowCount = 0
			case "duplicate":
				content = append(content, content...)
				manifest.Tables[0].RowCount = 2
			}
			manifest.Tables[0].SHA256 = fmt.Sprintf("%x", sha256.Sum256(content))
			if err := os.WriteFile(filepath.Join(dir, manifest.Tables[0].File), content, 0600); err != nil {
				t.Fatal(err)
			}
			metadata, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), metadata, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := prepareLogicalBackup(dir, testDB); err == nil {
				t.Fatal("inconsistent actual schema version accepted")
			}
		})
	}
}

func TestDecodeLogicalValueRejectsCoercionAndOverflow(t *testing.T) {
	for _, test := range []struct {
		name   string
		column logicalColumn
		raw    string
	}{
		{"quoted_integer", logicalColumn{Type: "integer", Bits: 64}, `"9007199254740993"`},
		{"integer_overflow", logicalColumn{Type: "integer", Bits: 64}, `9223372036854775808`},
		{"fractional_integer", logicalColumn{Type: "integer", Bits: 64}, `1.5`},
		{"unsigned_overflow", logicalColumn{Type: "unsigned_integer", Bits: 64}, `18446744073709551615`},
		{"negative_unsigned", logicalColumn{Type: "unsigned_integer", Bits: 64}, `-1`},
		{"small_integer_overflow", logicalColumn{Type: "integer", Bits: 8}, `128`},
		{"numeric_boolean", logicalColumn{Type: "boolean"}, `1`},
		{"infinite_float", logicalColumn{Type: "float"}, `1e9999`},
		{"non_microsecond_time", logicalColumn{Type: "timestamp"}, `"2026-10-07T04:34:56.123456789Z"`},
		{"non_utc_time", logicalColumn{Type: "timestamp"}, `"2026-10-07T12:34:56.123457+08:00"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeLogicalValue(test.column, json.RawMessage(test.raw)); err == nil {
				t.Fatal("invalid or lossy conversion accepted")
			}
		})
	}
}

func TestValidateJSONUnicodeRejectsLossyReplacement(t *testing.T) {
	for _, test := range []struct {
		name, raw string
		valid     bool
	}{
		{"high_surrogate", `"\ud800"`, false},
		{"low_surrogate", `"\udc00"`, false},
		{"high_then_non_surrogate", `"\ud800\u0041"`, false},
		{"low_then_high", `"\udc00\ud800"`, false},
		{"high_then_plain", `"\ud800text"`, false},
		{"surrogate_pair", `"\ud83d\ude42"`, true},
		{"literal_escape", `"\\ud800"`, true},
		{"ordinary_unicode_escape", `"\u4e2d\u6587"`, true},
		{"literal_replacement_character", `"�"`, true},
		{"escaped_quote", `"quote\"🙂"`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateJSONUnicode([]byte(test.raw))
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			_, err = decodeLogicalValue(logicalColumn{Type: "text"}, json.RawMessage(test.raw))
			if (err == nil) != test.valid {
				t.Fatalf("decoder valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestLogicalBackupUsesSingleSnapshotAcrossTables(t *testing.T) {
	setupBackupTest(t)
	models.AllTables = []any{backupTestItem{}, backupOtherTestItem{}}
	dsn := filepath.Join(t.TempDir(), "snapshot.sqlite") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	open := func() *gorm.DB {
		database, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		connection, err := database.DB()
		if err != nil {
			t.Fatal(err)
		}
		connection.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = connection.Close() })
		return database
	}
	source := open()
	writer := source
	if err := source.AutoMigrate(&models.Migrator{}, &backupTestItem{}, &backupOtherTestItem{}); err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&backupTestItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := source.Create(&backupOtherTestItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	var writeErr error
	if err := writeLogicalBackup(source, dir, func(table logicalTable) {
		if table.Name == "backup_test_items" {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var count int64
			if writeErr = writer.WithContext(ctx).Model(&backupTestItem{}).Count(&count).Error; writeErr != nil {
				return
			}
			writeErr = writer.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
				for _, name := range []string{"backup_test_items", "backup_other_test_items"} {
					if err := tx.Table(name).Where("id = ?", 1).Update("name", "after").Error; err != nil {
						return err
					}
				}
				return nil
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	manifest, err := prepareLogicalBackup(dir, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range manifest.Tables {
		if err := readLogicalRows(dir, table, func(row map[string]any) error {
			if row["name"] != "before" {
				return fmt.Errorf("snapshot mixed old and new values")
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	var changed backupOtherTestItem
	if err := writer.First(&changed).Error; err != nil || changed.Name != "after" {
		t.Fatalf("concurrent write was not committed: %v", err)
	}
}
