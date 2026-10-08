package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestorePreservesLocalBackupHistory(t *testing.T) {
	for _, format := range []string{"current", "legacy"} {
		t.Run(format, func(t *testing.T) {
			testRestorePreservesLocalBackupHistory(t, setupBackupTest(t), format)
		})
	}
}

func testRestorePreservesLocalBackupHistory(t *testing.T, conn *gorm.DB, format string) {
	t.Helper()
	current := seedHistoryRestoreState(t, conn)
	old := models.BackupRecord{
		BaseModel: models.BaseModel{ID: 10, CreatedAt: 100, UpdatedAt: 100},
		Status:    models.BackupStatusCompleted, FilePath: "/old-instance/deleted.zip", CompletedAt: 101,
	}
	if err := conn.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	dir := historyRestoreDirectory(t, conn, format)
	archive := zipHistoryRestoreDirectory(t, dir)
	// 模拟快照之后的清理与当前备份完成；恢复不能把这些本机变化倒回去。
	if err := conn.Delete(&old).Error; err != nil {
		t.Fatal(err)
	}
	current.Status, current.FilePath, current.CompletedAt = models.BackupStatusCompleted, archive, 300
	if err := conn.Save(&current).Error; err != nil {
		t.Fatal(err)
	}
	local := models.BackupRecord{
		BaseModel: models.BaseModel{ID: 30}, Status: models.BackupStatusFailed,
		CreatedReason: "target instance only", FailureReason: "local backup failed",
	}
	if err := conn.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	reserveHistorySequence(t, conn)
	before := readHistoryRestoreRecords(t, conn)
	if err := conn.Model(&backupTestItem{}).Where("id = ?", 7).Update("name", "newer local data").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&models.BackupConfig{}).Where("id = ?", 1).
		Updates(map[string]any{"backup_retention": 99, "backup_max_count": 42}).Error; err != nil {
		t.Fatal(err)
	}
	for attempt := range 2 {
		if err := Restore(archive); err != nil {
			t.Fatalf("restore attempt %d: %v", attempt+1, err)
		}
		if got := readHistoryRestoreRecords(t, conn); !reflect.DeepEqual(got, before) {
			t.Fatalf("restore changed local history: got %+v, want %+v", got, before)
		}
		assertHistoryRestoreBusinessState(t, conn, "archived business data", 31, 6)
	}
	assertHistorySequencePreserved(t, conn)
}

func TestRestoreValidatesArchivedLocalHistoryBeforeMaintenance(t *testing.T) {
	for _, scenario := range []struct{ format, damage string }{
		{"current", "checksum"}, {"current", "row_type"}, {"legacy", "row_type"},
	} {
		t.Run(scenario.format+"_"+scenario.damage, func(t *testing.T) {
			testRestoreValidatesArchivedLocalHistory(t, setupBackupTest(t), scenario.format, scenario.damage)
		})
	}
}

func testRestoreValidatesArchivedLocalHistory(t *testing.T, conn *gorm.DB, format, damage string) {
	t.Helper()
	seedHistoryRestoreState(t, conn)
	dir := historyRestoreDirectory(t, conn, format)
	if format == "current" {
		manifest, err := prepareLogicalBackup(dir, conn)
		if err != nil {
			t.Fatal(err)
		}
		for i := range manifest.Tables {
			table := &manifest.Tables[i]
			if table.Name != "backup_record" {
				continue
			}
			if damage == "checksum" {
				table.SHA256 = strings.Repeat("0", 64)
			} else {
				content := corruptHistoryRestoreRow(t, filepath.Join(dir, table.File))
				// 保持校验和与行数正确，单独验证被忽略表的行类型仍受检查。
				table.SHA256 = fmt.Sprintf("%x", sha256.Sum256(content))
			}
		}
		metadata, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), metadata, 0600); err != nil {
			t.Fatal(err)
		}
	} else {
		corruptHistoryRestoreRow(t, filepath.Join(dir, "BackupRecord.json"))
	}
	before := readHistoryRestoreRecords(t, conn)
	enteredMaintenance := false
	beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
		enteredMaintenance = true
		return &restoreMaintenance{database: conn}, nil
	}
	if err := Restore(zipHistoryRestoreDirectory(t, dir)); !errors.Is(err, ErrArchiveInvalid) {
		t.Fatalf("invalid ignored history error = %v, want ErrArchiveInvalid", err)
	}
	if enteredMaintenance {
		t.Fatal("invalid ignored history entered maintenance")
	}
	result := GetRunningResult()
	if result.RestoreOutcome != "not_started" || result.RestartRequired {
		t.Fatalf("invalid preflight result: %+v", result)
	}
	if got := readHistoryRestoreRecords(t, conn); !reflect.DeepEqual(got, before) {
		t.Fatalf("invalid archive changed local history: got %+v, want %+v", got, before)
	}
	assertHistoryRestoreBusinessState(t, conn, "archived business data", 31, 6)
}

func TestRestoreFailurePreservesLocalBackupHistory(t *testing.T) {
	for _, format := range []string{"current", "legacy"} {
		t.Run(format, func(t *testing.T) {
			testRestoreFailurePreservesLocalBackupHistory(t, setupBackupTest(t), format)
		})
	}
}

func testRestoreFailurePreservesLocalBackupHistory(t *testing.T, conn *gorm.DB, format string) {
	t.Helper()
	current := seedHistoryRestoreState(t, conn)
	archive := zipHistoryRestoreDirectory(t, historyRestoreDirectory(t, conn, format))
	current.Status, current.FilePath, current.CompletedAt = models.BackupStatusCompleted, archive, 300
	if err := conn.Save(&current).Error; err != nil {
		t.Fatal(err)
	}
	reserveHistorySequence(t, conn)
	before := readHistoryRestoreRecords(t, conn)
	if err := conn.Model(&backupTestItem{}).Where("id = ?", 7).Update("name", "keep local data").Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&models.BackupConfig{}).Where("id = ?", 1).
		Updates(map[string]any{"backup_retention": 99, "backup_max_count": 42}).Error; err != nil {
		t.Fatal(err)
	}
	importedConfig, failedInsert := false, false
	if err := conn.Callback().Create().Before("gorm:create").Register("history-restore-failure", func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "backup_config":
			importedConfig = true
		case "backup_test_items":
			failedInsert = true
			tx.AddError(errors.New("injected business insert failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive); err == nil {
		t.Fatal("business insert failure unexpectedly restored")
	}
	if !importedConfig || !failedInsert || GetRunningResult().RestoreOutcome != "rolled_back" {
		t.Fatalf("restore did not exercise a late rollback: config=%t failure=%t result=%+v", importedConfig, failedInsert, GetRunningResult())
	}
	if got := readHistoryRestoreRecords(t, conn); !reflect.DeepEqual(got, before) {
		t.Fatalf("failed restore changed local history: got %+v, want %+v", got, before)
	}
	assertHistoryRestoreBusinessState(t, conn, "keep local data", 99, 42)
	assertHistorySequencePreserved(t, conn)
}

func seedHistoryRestoreState(t *testing.T, conn *gorm.DB) models.BackupRecord {
	t.Helper()
	models.AllTables = []any{&models.BackupRecord{}, &models.BackupConfig{}, &models.Migrator{}, &backupTestItem{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	var versions int64
	if err := conn.Model(&models.Migrator{}).Count(&versions).Error; err != nil {
		t.Fatal(err)
	}
	if versions == 0 {
		if err := conn.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
			t.Fatal(err)
		}
	}
	current := models.BackupRecord{
		BaseModel: models.BaseModel{ID: 20, CreatedAt: 200, UpdatedAt: 200},
		Status:    models.BackupStatusRunning, BackupType: models.BackupTypeManual,
		CreatedReason: "archive snapshot before terminal result",
	}
	for _, value := range []any{
		&current,
		&models.BackupConfig{BaseModel: models.BaseModel{ID: 1}, BackupRetention: 31, BackupMaxCount: 6},
		&backupTestItem{ID: 7, Name: "archived business data"},
	} {
		if err := conn.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	return current
}

func historyRestoreDirectory(t *testing.T, conn *gorm.DB, format string) string {
	t.Helper()
	dir := t.TempDir()
	if format == "current" {
		if err := writeLogicalBackup(conn, dir, nil); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	// 旧格式按模型 JSON 编码，避免用当前列编码器伪造兼容测试。
	for _, model := range models.AllTables {
		rows := reflect.New(reflect.SliceOf(reflect.TypeOf(model).Elem()))
		if err := conn.Find(rows.Interface()).Error; err != nil {
			t.Fatal(err)
		}
		var data bytes.Buffer
		for i := 0; i < rows.Elem().Len(); i++ {
			if err := json.NewEncoder(&data).Encode(rows.Elem().Index(i).Interface()); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(dir, helpers.GetStructName(model)+".json"), data.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func zipHistoryRestoreDirectory(t *testing.T, dir string) string {
	t.Helper()
	archive := filepath.Join(t.TempDir(), "history.zip")
	if err := helpers.ZipDir(dir, archive); err != nil {
		t.Fatal(err)
	}
	return archive
}

func corruptHistoryRestoreRow(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var row map[string]json.RawMessage
	if err := json.Unmarshal(data, &row); err != nil {
		t.Fatal(err)
	}
	row["file_size"] = json.RawMessage(`"invalid integer"`)
	data, err = json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func readHistoryRestoreRecords(t *testing.T, conn *gorm.DB) []models.BackupRecord {
	t.Helper()
	var records []models.BackupRecord
	if err := conn.Order("id").Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	return records
}

func assertHistoryRestoreBusinessState(t *testing.T, conn *gorm.DB, name string, retention, maxCount int) {
	t.Helper()
	var item backupTestItem
	if err := conn.First(&item, 7).Error; err != nil || item.Name != name {
		t.Fatalf("business data = %+v, error = %v, want name %q", item, err, name)
	}
	var config models.BackupConfig
	if err := conn.First(&config, 1).Error; err != nil || config.BackupRetention != retention || config.BackupMaxCount != maxCount {
		t.Fatalf("backup config = %+v, error = %v, want retention=%d maximum=%d", config, err, retention, maxCount)
	}
}

func reserveHistorySequence(t *testing.T, conn *gorm.DB) {
	t.Helper()
	reserved := models.BackupRecord{Status: models.BackupStatusCompleted}
	if conn.Dialector.Name() == "postgres" {
		if err := conn.Exec("ALTER SEQUENCE backup_record_id_seq RESTART WITH 1000").Error; err != nil {
			t.Fatal(err)
		}
	} else {
		reserved.ID = 1000
	}
	if err := conn.Create(&reserved).Error; err != nil {
		t.Fatal(err)
	}
	if reserved.ID != 1000 {
		t.Fatalf("reserved history ID = %d, want 1000", reserved.ID)
	}
	if err := conn.Delete(&reserved).Error; err != nil {
		t.Fatal(err)
	}
}

func assertHistorySequencePreserved(t *testing.T, conn *gorm.DB) {
	t.Helper()
	next := models.BackupRecord{Status: models.BackupStatusPending}
	if err := conn.Create(&next).Error; err != nil || next.ID != 1001 {
		t.Fatalf("history sequence lost its allocated high-water value: ID=%d error=%v, want 1001", next.ID, err)
	}
}
