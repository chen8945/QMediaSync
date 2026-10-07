//go:build integration

package backup

import (
	"archive/zip"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type crossEngineRecord struct {
	model  any
	id     uint
	values map[string]any
}

// 两端均初始化全部正式模型，使用真实入口检验归档与不同数据库驱动的共同契约。
func TestLogicalBackupRestoreCrossEngine(t *testing.T) {
	if os.Getenv("QMS_TEST_POSTGRES_DSN") == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for isolated cross-engine tests")
	}
	for _, sourceEngine := range []string{"sqlite", "postgres"} {
		targetEngine := "postgres"
		if sourceEngine == "postgres" {
			targetEngine = "sqlite"
		}
		t.Run(sourceEngine+"_to_"+targetEngine, func(t *testing.T) {
			// 实例密钥仅在启动时加载，各方向独立进程避免共享其他用例的进程密钥。
			if os.Getenv("QMS_CROSS_ENGINE_PROCESS") != sourceEngine {
				command := exec.Command(os.Args[0], "-test.run=^TestLogicalBackupRestoreCrossEngine$/^"+sourceEngine+"_to_"+targetEngine+"$")
				command.Env = append(os.Environ(), "QMS_CROSS_ENGINE_PROCESS="+sourceEngine)
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("cross-engine subprocess failed: %v\n%s", err, output)
				}
				return
			}
			allTables := models.AllTables
			setupBackupTest(t)
			models.AllTables = allTables
			source := openCrossEngineDatabase(t, sourceEngine)
			target := openCrossEngineDatabase(t, targetEngine)
			for _, conn := range []*gorm.DB{source, target} {
				db.Db = conn
				if existed, err := models.InitDB(); err != nil || existed {
					t.Fatalf("initialize %s: existing=%t, err=%v", conn.Dialector.Name(), existed, err)
				}
			}
			db.Db = source
			// 导出前初始化配置，避免备份收尾新增配置行后与快照行数不一致。
			models.GetBackupService()
			records := seedCrossEngineRecords(t, source)
			if err := helpers.InitEncryptionKey(); err != nil {
				t.Fatal(err)
			}
			if err := Backup(models.BackupTypeManual, "cross-engine regression"); err != nil {
				t.Fatal(err)
			}
			var backupRecord models.BackupRecord
			if err := source.Order("id DESC").First(&backupRecord).Error; err != nil {
				t.Fatal(err)
			}
			manifest := readCrossEngineManifest(t, backupRecord.FilePath)
			if manifest.SourceEngine != sourceEngine || manifest.SchemaVersion != models.MaxVersionCode {
				t.Fatalf("unexpected archive metadata: engine=%s, version=%d", manifest.SourceEngine, manifest.SchemaVersion)
			}
			// 目标库的已有浏览器会话同样必须清除。
			if err := target.Create(&models.UserSession{SessionID: "target-browser", TokenID: "target-token"}).Error; err != nil {
				t.Fatal(err)
			}
			db.Db = target
			models.GlobalBackupService = nil
			if err := Restore(backupRecord.FilePath); err != nil {
				t.Fatal(err)
			}
			assertCrossEngineSchemaAndCounts(t, source, target, manifest)
			for _, record := range records {
				assertCrossEngineRecord(t, target, record)
			}
			var user models.User
			if err := target.First(&user, 41).Error; err != nil || !user.IsTwoFactorEnabled() {
				t.Fatalf("restored TOTP is not enabled: %v", err)
			}
			// 新进程从复制后的 config/ 读取原密钥，证明无需修改验证器。
			copiedConfig := t.TempDir()
			for _, name := range []string{"encryption.key", "secret.txt"} {
				content, err := os.ReadFile(filepath.Join(helpers.ConfigDir, name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(copiedConfig, name), content, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(copiedConfig, "secret.txt"), []byte(user.TwoFactorSecret), 0600); err != nil {
				t.Fatal(err)
			}
			runCrossEngineSecretProcess(t, copiedConfig, "decrypt")

			if !target.Migrator().HasConstraint(&models.ApiKey{}, "User") {
				t.Fatal("API key owner foreign key disappeared")
			}
			if err := target.Model(&models.ApiKey{}).Where("id = ?", 42).UpdateColumn("user_id", 999999).Error; err == nil {
				t.Fatal("restored API key foreign key accepts an absent user")
			}
			next := models.SyncFile{}
			if err := target.Create(&next).Error; err != nil || next.ID != uint(9007199254740994) {
				t.Fatalf("next generated primary key does not follow restored large ID: %d, %v", next.ID, err)
			}
		})
	}
}

func openCrossEngineDatabase(t *testing.T, engine string) *gorm.DB {
	t.Helper()
	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	var conn *gorm.DB
	var err error
	if engine == "sqlite" {
		conn, err = gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "database.sqlite")), config)
	} else {
		dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
		parsed, parseErr := url.Parse(dsn)
		if parseErr != nil || parsed.User == nil || parsed.Host == "" || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") {
			t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
		}
		admin, openErr := sql.Open("postgres", dsn)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() { admin.Close() })
		schema := fmt.Sprintf("qms_cross_backup_%d_%d", os.Getpid(), time.Now().UnixNano())
		if _, err := admin.Exec("CREATE SCHEMA " + schema); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec("DROP SCHEMA " + schema + " CASCADE"); err != nil {
				t.Error(err)
			}
		})
		query := parsed.Query()
		query.Set("search_path", schema)
		query.Set("statement_timeout", "15000")
		parsed.RawQuery = query.Encode()
		sqlDB, openErr := sql.Open("postgres", parsed.String())
		if openErr != nil {
			t.Fatal(openErr)
		}
		// 与生产使用同一个 lib/pq 驱动；DriverName 也影响 GORM 的参数处理。
		conn, err = gorm.Open(postgres.New(postgres.Config{DriverName: "postgres", Conn: sqlDB}), config)
		if err != nil {
			sqlDB.Close()
		}
	}
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	if engine == "sqlite" {
		if err := conn.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
			t.Fatal(err)
		}
	}
	return conn
}

func seedCrossEngineRecords(t *testing.T, conn *gorm.DB) []crossEngineRecord {
	t.Helper()
	runCrossEngineSecretProcess(t, helpers.ConfigDir, "encrypt")
	ciphertext, err := os.ReadFile(filepath.Join(helpers.ConfigDir, "secret.txt"))
	if err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 10, 7, 12, 34, 56, 123456789, time.FixedZone("UTC+8", 8*60*60))
	records := []crossEngineRecord{
		{&models.User{}, 41, map[string]any{"singleton_key": uint8(1), "username": "cross-engine-admin", "password": "test-password-hash", "two_factor_enabled": true, "two_factor_secret": string(ciphertext), "two_factor_pending_secret": string(ciphertext)}},
		{&models.ApiKey{}, 42, map[string]any{"user_id": uint(41), "name": "disabled key", "key_hash": models.HashAPIKey("qms_test_disabled"), "key_prefix": "qms_test", "is_active": false}},
		{&models.ApiKey{}, 43, map[string]any{"user_id": uint(41), "name": "active key", "key_hash": models.HashAPIKey("qms_test_active"), "key_prefix": "qms_test", "is_active": true}},
		{&models.Sync{}, 51, map[string]any{"status": int(models.SyncStatusCompleted), "ledger_error": nil, "ledger_status": nil, "ledger_finished_at": nil, "scan_result": nil, "updated_at": int64(0)}},
		{&models.SyncFile{}, uint(9007199254740993), map[string]any{"file_size": int64(math.MaxInt64), "file_id": "90071992547409931234", "openlist_object_id": "remote-object", "openlist_sha1": strings.Repeat("a", 40), "openlist_md5": strings.Repeat("b", 32), "local_file_path": "/媒体/电影.strm"}},
		{&models.DbDownloadTask{}, 61, map[string]any{"status": int(models.DownloadStatusCompleted), "remote_download_url": "https://example.invalid/private", "emby_item_id": "item-1", "local_source_path": "/媒体/source.nfo", "dedup_scope_hash": "scope", "dedup_locator_hash": "locator", "replace_baseline": "baseline", "published_sha256": strings.Repeat("c", 64)}},
		{&models.DbUploadTask{}, 62, map[string]any{"status": int(models.UploadStatusCompleted), "remote_file_id": "uploaded-object", "remote_full_path": "/media/uploaded.mkv", "source_cleanup_error": strings.Repeat("原始错误", 100)}},
		{&models.UploadSession{}, 63, map[string]any{"upload_task_id": uint(62), "upload_id": "oss-checkpoint", "uploaded_parts": 3}},
		{&models.Media{}, 81, map[string]any{"actors_json": `[{"name":"演员"}]`, "genres_json": `["剧情"]`, "subtitle_file_json": `[{"path":"/字幕/中文.srt"}]`}},
		{&models.MediaSeason{}, 82, map[string]any{"vote_average": 7.875456789012345}},
		{&models.NotificationChannel{}, 83, map[string]any{"channel_type": "webhook", "channel_name": "disabled notification", "is_enabled": false, "created_at": when, "updated_at": when}},
		{&models.Account{}, 91, map[string]any{"name": "long credential account", "token": strings.Repeat("t", 2048), "refresh_token": strings.Repeat("r", 2048), "username": strings.Repeat("用", 100), "token_failed_reason": strings.Repeat("远端错误", 200)}},
	}
	for _, record := range records {
		values := make(map[string]any, len(record.values)+1)
		for column, value := range record.values {
			values[column] = value
		}
		values["id"] = record.id
		if err := conn.Model(record.model).Create(values).Error; err != nil {
			t.Fatalf("seed %T: %v", record.model, err)
		}
	}
	settings := crossEngineRecord{&models.Settings{}, 1, map[string]any{"video_ext": `["mkv","mp4"]`, "meta_ext": `["nfo"]`, "exclude_name": `["sample"]`, "exclude_name_regex": `["(?i)trailer"]`, "delete_dir": 0, "url_validity_check_enabled": 0}}
	if err := conn.Model(settings.model).Where("id = ?", settings.id).Updates(settings.values).Error; err != nil {
		t.Fatal(err)
	}
	records = append(records, settings)
	if err := conn.Create(&models.UserSession{UserID: 41, SessionID: "source-browser", TokenID: "source-token", CSRFTokenHash: "source-csrf"}).Error; err != nil {
		t.Fatal(err)
	}
	return records
}

func readCrossEngineManifest(t *testing.T, path string) logicalManifest {
	t.Helper()
	archive, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	for _, file := range archive.File {
		if strings.Contains(file.Name, "UserSession") || strings.Contains(file.Name, "user_sessions") {
			t.Fatal("browser session data appeared in backup archive")
		}
	}
	file, err := archive.Open("manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	var manifest logicalManifest
	if err := json.NewDecoder(file).Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}

func assertCrossEngineSchemaAndCounts(t *testing.T, source, target *gorm.DB, manifest logicalManifest) {
	t.Helper()
	archived := make(map[string]int64, len(manifest.Tables))
	for _, table := range manifest.Tables {
		archived[table.Name] = table.RowCount
	}
	for _, model := range models.AllTables {
		stmt := &gorm.Statement{DB: target}
		if err := stmt.Parse(model); err != nil {
			t.Fatal(err)
		}
		if !target.Migrator().HasTable(model) {
			t.Errorf("registered table %s was not created", stmt.Schema.Table)
			continue
		}
		rows, err := target.Table(stmt.Schema.Table).Where("1 = 0").Rows()
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if closeErr := rows.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
		if err != nil {
			t.Fatal(err)
		}
		storedColumns := make(map[string]bool, len(columns))
		for _, column := range columns {
			storedColumns[column] = true
		}
		for _, column := range stmt.Schema.DBNames {
			if !storedColumns[column] {
				t.Errorf("registered persistent column %s.%s missing after restore", stmt.Schema.Table, column)
			}
		}
		if stmt.Schema.Table == "user_sessions" {
			var count int64
			if err := target.Model(model).Count(&count).Error; err != nil || count != 0 {
				t.Errorf("browser sessions survived restore: %d, %v", count, err)
			}
			continue
		}
		switch model.(type) {
		case models.EmbyObservedEvidenceIndex, *models.EmbyObservedEvidenceIndex, models.EmbyItemMembership, *models.EmbyItemMembership:
			continue // 派生索引有独立原始数据重建回归。
		}
		want, ok := archived[stmt.Schema.Table]
		if !ok {
			t.Errorf("registered business table %s missing from archive", stmt.Schema.Table)
			continue
		}
		for _, conn := range []*gorm.DB{source, target} {
			var count int64
			if err := conn.Model(model).Count(&count).Error; err != nil || count != want {
				t.Errorf("%s %s row count: got=%d, want=%d, err=%v", conn.Dialector.Name(), stmt.Schema.Table, count, want, err)
			}
		}
	}
	for _, index := range []struct {
		model any
		name  string
	}{
		{&models.DbDownloadTask{}, "idx_db_download_tasks_active_target"},
		{&models.DbUploadTask{}, "idx_db_upload_tasks_active_target"},
		{&models.StrmGenerationTask{}, "idx_strm_generation_tasks_queue"},
	} {
		if !target.Migrator().HasIndex(index.model, index.name) {
			t.Errorf("restored schema lacks index %s", index.name)
		}
	}
}

func assertCrossEngineRecord(t *testing.T, conn *gorm.DB, record crossEngineRecord) {
	t.Helper()
	stmt := &gorm.Statement{DB: conn}
	if err := stmt.Parse(record.model); err != nil {
		t.Fatal(err)
	}
	for column, want := range record.values {
		query := "SELECT " + stmt.Quote(column) + " FROM " + stmt.Quote(stmt.Schema.Table) + " WHERE id = ?"
		if want == nil {
			var got any
			if err := conn.Raw(query, record.id).Row().Scan(&got); err != nil || got != nil {
				t.Errorf("%s.%s lost SQL NULL: %v", stmt.Schema.Table, column, err)
			}
			continue
		}
		got := reflect.New(reflect.TypeOf(want))
		if err := conn.Raw(query, record.id).Row().Scan(got.Interface()); err != nil {
			t.Errorf("read %s.%s: %v", stmt.Schema.Table, column, err)
			continue
		}
		if timestamp, ok := want.(time.Time); ok {
			if !got.Elem().Interface().(time.Time).Equal(timestamp.UTC().Round(time.Microsecond)) {
				t.Errorf("%s.%s did not preserve UTC microsecond timestamp", stmt.Schema.Table, column)
			}
		} else if !reflect.DeepEqual(got.Elem().Interface(), want) {
			t.Errorf("%s.%s changed during cross-engine restore", stmt.Schema.Table, column)
		}
	}
}

func runCrossEngineSecretProcess(t *testing.T, configDir, operation string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestLogicalCrossEngineSecretProcess$")
	command.Env = append(os.Environ(), "QMS_BACKUP_SECRET_TEST_CONFIG="+configDir, "QMS_BACKUP_SECRET_TEST_OPERATION="+operation)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("separate-process local secret %s failed: %v\n%s", operation, err, output)
	}
}

func TestLogicalCrossEngineSecretProcess(t *testing.T) {
	configDir := os.Getenv("QMS_BACKUP_SECRET_TEST_CONFIG")
	if configDir == "" {
		t.Skip("helper subprocess for persisted config encryption key")
	}
	helpers.ConfigDir = configDir
	const originalSecret = "JBSWY3DPEHPK3PXP"
	path := filepath.Join(configDir, "secret.txt")
	if os.Getenv("QMS_BACKUP_SECRET_TEST_OPERATION") == "encrypt" {
		encrypted, err := helpers.EncryptLocalSecret(originalSecret)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(encrypted), 0600); err != nil {
			t.Fatal(err)
		}
		return
	}
	ciphertext, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := helpers.DecryptLocalSecret(string(ciphertext))
	if err != nil || secret != originalSecret {
		t.Fatal("original config key cannot decrypt restored TOTP secret")
	}
}
