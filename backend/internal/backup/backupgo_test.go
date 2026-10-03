package backup

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type backupTestItem struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

type backupOtherTestItem struct {
	ID   uint `gorm:"primaryKey"`
	Name string
}

// 保留 SQLite 的真实存储，用不支持的 setval 模拟 PostgreSQL 序列修复失败。
type postgresSequenceFailureDialect struct{ gorm.Dialector }

func (postgresSequenceFailureDialect) Name() string { return "postgres" }

func setupBackupTest(t *testing.T) *gorm.DB {
	t.Helper()
	originalDB, originalLogger, originalDir := db.Db, helpers.AppLogger, helpers.ConfigDir
	originalTables, originalService := models.AllTables, models.GlobalBackupService
	originalPause, originalResume, originalZip := pauseTasks, resumeTasks, zipDir
	originalResult := *GetRunningResult()
	progressMu.Lock()
	runningResult = BackupOrRestoreResult{Status: "idle"}
	progressMu.Unlock()
	pauseTasks, resumeTasks = func() error { return nil }, func() error { return nil }
	models.AllTables = []any{&backupTestItem{}}
	models.GlobalBackupService = nil
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Db = testDB
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	helpers.ConfigDir = t.TempDir()
	t.Cleanup(func() {
		pauseTasks, resumeTasks, zipDir = originalPause, originalResume, originalZip
		models.AllTables, models.GlobalBackupService = originalTables, originalService
		progressMu.Lock()
		runningResult = originalResult
		progressMu.Unlock()
		db.Db, helpers.AppLogger, helpers.ConfigDir = originalDB, originalLogger, originalDir
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := testDB.AutoMigrate(&backupTestItem{}, &backupOtherTestItem{}, &models.BackupRecord{}, &models.BackupConfig{}); err != nil {
		t.Fatal(err)
	}
	return testDB
}

func TestBackupTerminalStatusAndHistory(t *testing.T) {
	for _, scenario := range []string{"success", "root_directory", "record_create", "record_directory", "query", "zip", "zip_partial", "record_finish", "panic", "resume"} {
		t.Run(scenario, func(t *testing.T) {
			testDB := setupBackupTest(t)
			backupType := models.BackupTypeManual
			switch scenario {
			case "root_directory":
				if err := os.WriteFile(filepath.Join(helpers.ConfigDir, "backups"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "record_create":
				if err := testDB.Migrator().DropTable(&models.BackupRecord{}); err != nil {
					t.Fatal(err)
				}
			case "record_directory":
				if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(helpers.ConfigDir, "backups", "1"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			case "query":
				if err := testDB.Migrator().DropTable(&backupTestItem{}); err != nil {
					t.Fatal(err)
				}
			case "zip":
				backupType = "missing/subdirectory"
			case "zip_partial":
				// 模拟 ZIP 已创建但收尾写入失败。
				zipDir = func(_, dst string) error {
					if err := os.WriteFile(dst, []byte("partial"), 0600); err != nil {
						return err
					}
					return errors.New("zip close failed")
				}
			case "record_finish":
				if err := testDB.Exec(`CREATE TRIGGER fail_record_update BEFORE UPDATE ON backup_record BEGIN SELECT RAISE(ABORT, 'write failed'); END`).Error; err != nil {
					t.Fatal(err)
				}
			case "panic":
				pauseTasks = func() error { panic("private-secret") }
			case "resume":
				resumeTasks = func() error { return errors.New("private-secret") }
			}
			err := Backup(backupType, "test")
			want := models.BackupStatusFailed
			if scenario == "success" {
				want = models.BackupStatusCompleted
			}
			if (err == nil) != (want == models.BackupStatusCompleted) {
				t.Fatalf("error=%v，场景=%s", err, scenario)
			}
			result := GetRunningResult()
			if result.IsRunning || result.Status != want {
				t.Fatalf("错误终态：%+v", result)
			}
			if strings.Contains(result.ErrorMsg, "private-secret") || strings.Contains(result.ErrorMsg, helpers.ConfigDir) {
				t.Fatalf("公开错误泄露内部信息：%+v", result)
			}
			if scenario == "zip_partial" {
				if archives, _ := filepath.Glob(filepath.Join(helpers.ConfigDir, "backups", "*.zip")); len(archives) != 0 {
					t.Fatalf("打包失败后残留归档：%v", archives)
				}
			}
			if scenario != "root_directory" && scenario != "record_create" && scenario != "record_finish" {
				var record models.BackupRecord
				if err := testDB.First(&record).Error; err != nil {
					t.Fatal(err)
				}
				if record.Status != want {
					t.Fatalf("历史状态=%s，want=%s", record.Status, want)
				}
				if scenario == "success" {
					archive, err := zip.OpenReader(record.FilePath)
					if err != nil {
						t.Fatal(err)
					}
					defer archive.Close()
					if len(archive.File) != 1 {
						t.Fatalf("备份文件数=%d", len(archive.File))
					}
				}
			}
		})
	}
}

func TestBackupArchiveRoundTrip(t *testing.T) {
	testDB := setupBackupTest(t)
	want := backupTestItem{ID: 1, Name: strings.Repeat("媒体记录", 100)}
	if err := testDB.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	if err := Backup(models.BackupTypeManual, "round trip"); err != nil {
		t.Fatal(err)
	}
	var record models.BackupRecord
	if err := testDB.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := testDB.Model(&backupTestItem{}).Where("id = ?", want.ID).Update("name", "changed").Error; err != nil {
		t.Fatal(err)
	}
	if err := Restore(record.FilePath); err != nil {
		t.Fatal(err)
	}
	var got backupTestItem
	if err := testDB.First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("restored=%+v, want %+v", got, want)
	}
}

func writeBackupArchive(t *testing.T, files map[string]string, method uint16) string {
	t.Helper()
	archivePath := filepath.Join(t.TempDir(), "input.zip")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for name, content := range files {
		entry, err := writer.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(entry, content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return archivePath
}

func TestRestoreTerminalStatusAndPartialFailure(t *testing.T) {
	for _, scenario := range []string{"success", "old_store", "old_missing_table", "invalid_zip", "empty_zip", "invalid_json", "insert", "scanner", "temp_directory", "sequence"} {
		t.Run(scenario, func(t *testing.T) {
			testDB := setupBackupTest(t)
			if scenario == "sequence" {
				testDB.Dialector = postgresSequenceFailureDialect{testDB.Dialector}
			}
			models.AllTables = []any{&backupTestItem{}, &backupOtherTestItem{}}
			if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"backupTestItem.json":      "{\"ID\":1,\"Name\":\"restored\"}\n",
				"backupOtherTestItem.json": "{\"ID\":2,\"Name\":\"other\"}\n",
			}
			switch scenario {
			case "old_missing_table":
				delete(files, "backupOtherTestItem.json")
				if err := testDB.Create(&backupOtherTestItem{ID: 3, Name: "kept"}).Error; err != nil {
					t.Fatal(err)
				}
			case "empty_zip":
				files = map[string]string{}
			case "invalid_json":
				files["backupTestItem.json"] = "invalid\n" + files["backupTestItem.json"]
			case "insert":
				files["backupTestItem.json"] += files["backupTestItem.json"]
			case "scanner":
				files["backupTestItem.json"] = strings.Repeat("x", 17*1024*1024)
			}
			method := uint16(zip.Deflate)
			if scenario == "old_store" {
				method = zip.Store
			}
			archivePath := writeBackupArchive(t, files, method)
			if scenario == "invalid_zip" {
				if err := os.WriteFile(archivePath, []byte("invalid"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "temp_directory" {
				if err := os.Remove(filepath.Join(helpers.ConfigDir, "backups")); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(helpers.ConfigDir, "backups"), []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := Restore(archivePath)
			wantSuccess := scenario == "success" || scenario == "old_store" || scenario == "old_missing_table"
			if (err == nil) != wantSuccess {
				t.Fatalf("error=%v，场景=%s", err, scenario)
			}
			result := GetRunningResult()
			if result.IsRunning || (result.Status == models.BackupStatusCompleted) != wantSuccess {
				t.Fatalf("错误终态：%+v", result)
			}
			if !wantSuccess && (result.Status != models.BackupStatusFailed || result.ErrorMsg == "") {
				t.Fatalf("失败缺少安全说明：%+v", result)
			}
			if scenario == "invalid_json" || scenario == "insert" || scenario == "success" || scenario == "old_store" {
				var item backupTestItem
				var other backupOtherTestItem
				if err := testDB.First(&item).Error; err != nil {
					t.Fatal(err)
				}
				if err := testDB.First(&other).Error; err != nil {
					t.Fatal(err)
				}
				if item.Name != "restored" || other.Name != "other" {
					t.Fatalf("应继续恢复可用数据：%+v %+v", item, other)
				}
			}
			if scenario == "old_missing_table" {
				var other backupOtherTestItem
				if err := testDB.First(&other).Error; err != nil || other.Name != "kept" {
					t.Fatalf("缺失模型必须保留现存表：%+v %v", other, err)
				}
			}
		})
	}
}

func TestRestoreSyncFileSiblingPathIndex(t *testing.T) {
	for _, scenario := range []string{"rows", "empty", "index_failure", "identity_index_failure"} {
		t.Run(scenario, func(t *testing.T) {
			testDB := setupBackupTest(t)
			if scenario == "identity_index_failure" && testDB.Dialector.Name() != "sqlite" {
				t.Skip("文件身份索引仅用于 SQLite")
			}
			models.AllTables = []any{models.SyncFile{}, &backupOtherTestItem{}}
			if scenario == "empty" {
				models.AllTables[0] = &models.SyncFile{}
			}
			if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := testDB.AutoMigrate(&models.SyncFile{}); err != nil {
				t.Fatal(err)
			}
			if err := models.EnsureSyncFileLookupIndexes(testDB); err != nil {
				t.Fatal(err)
			}
			if err := testDB.Create(&models.SyncFile{FileId: "old"}).Error; err != nil {
				t.Fatal(err)
			}
			want := models.SyncFile{SyncPathId: 10, FileId: "restored", Path: "/媒体/目录", Uploaded: true, Processed: true}
			record, err := json.Marshal(want)
			if err != nil {
				t.Fatal(err)
			}
			files := map[string]string{
				"SyncFile.json":            string(record) + "\n",
				"backupOtherTestItem.json": "{\"ID\":2,\"Name\":\"other\"}\n",
			}
			if scenario == "empty" {
				files["SyncFile.json"] = ""
			}
			failedIndexName := "idx_sync_files_sibling_path"
			if scenario == "identity_index_failure" {
				failedIndexName = "idx_sync_files_identity"
			}
			failIndex := scenario == "index_failure" || scenario == "identity_index_failure"
			indexErr := errors.New("sync file index creation failed")
			if err := testDB.Callback().Raw().Before("gorm:raw").Register("test:fail_sibling_index", func(tx *gorm.DB) {
				if failIndex && strings.Contains(tx.Statement.SQL.String(), "CREATE INDEX IF NOT EXISTS "+failedIndexName) {
					tx.AddError(indexErr)
				}
			}); err != nil {
				t.Fatal(err)
			}
			err = Restore(writeBackupArchive(t, files, zip.Deflate))
			if failIndex {
				if !errors.Is(err, indexErr) || GetRunningResult().Status != models.BackupStatusFailed {
					t.Fatalf("索引失败必须让恢复报告失败：%v %+v", err, GetRunningResult())
				}
			} else if err != nil || GetRunningResult().Status != models.BackupStatusCompleted {
				t.Fatalf("恢复失败：%v %+v", err, GetRunningResult())
			}
			if testDB.Migrator().HasIndex(&models.SyncFile{}, "idx_sync_files_sibling_path") != (scenario != "index_failure") {
				t.Fatal("重建后的索引状态不正确")
			}
			if testDB.Migrator().HasIndex(&models.SyncFile{}, "idx_sync_files_identity") != (testDB.Dialector.Name() == "sqlite" && !failIndex) {
				t.Fatal("文件身份索引应仅在 SQLite 成功恢复后存在")
			}
			var rows []models.SyncFile
			if err := testDB.Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			if scenario == "empty" {
				if len(rows) != 0 {
					t.Fatalf("空备份应恢复为空表：%d", len(rows))
				}
			} else if len(rows) != 1 || rows[0].FileId != want.FileId || rows[0].Path != want.Path || !rows[0].Uploaded || !rows[0].Processed {
				t.Fatalf("索引失败时仍应导入可用记录：%+v", rows)
			}
			var other backupOtherTestItem
			if err := testDB.First(&other).Error; err != nil || other.Name != "other" {
				t.Fatalf("必须继续恢复其他表：%+v %v", other, err)
			}
		})
	}
}

func TestStartBackupReservesTaskBeforeReturning(t *testing.T) {
	setupBackupTest(t)
	paused, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	pauseTasks = func() error { close(paused); <-release; return nil }
	if err := StartBackup(models.BackupTypeManual, "test"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		deadline := time.Now().Add(5 * time.Second)
		for IsRunning() && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
	})
	if result := GetRunningResult(); !result.IsRunning || result.Status != models.BackupStatusRunning {
		t.Fatalf("接受请求时必须已设置本轮状态：%+v", result)
	}
	select {
	case <-paused:
	case <-time.After(5 * time.Second):
		t.Fatal("备份未到达暂停后台任务步骤")
	}
	if err := StartRestore("unused.zip", false); !errors.Is(err, ErrTaskRunning) {
		t.Fatalf("并发任务必须被拒绝：%v", err)
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(5 * time.Second)
	for IsRunning() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if IsRunning() {
		t.Fatal("后台任务没有结束")
	}
	if GetRunningResult().Status != models.BackupStatusCompleted {
		t.Fatal("备份应完成")
	}
}

func TestProgressSnapshotsAreConcurrentSafe(t *testing.T) {
	setupBackupTest(t)
	if err := beginTask("backup"); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		for count := range 1000 {
			SetRunningResult("backup", "运行中", 1000, count, "")
		}
	}()
	go func() {
		defer workers.Done()
		for range 1000 {
			data, err := json.Marshal(GetRunningResult())
			if err != nil || len(data) == 0 {
				t.Errorf("快照序列化失败：%v", err)
			}
		}
	}()
	workers.Wait()
	finishTask(nil)
}

func resultStatus(t *testing.T, result *BackupOrRestoreResult) string {
	t.Helper()
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatal(err)
	}
	return body.Status
}

func TestRestoreMissingFilePublishesFailure(t *testing.T) {
	setupBackupTest(t)
	SetRunningResult("backup", "旧备份已完成", 1, 1, "")
	if err := Restore(filepath.Join(t.TempDir(), "missing.zip")); err == nil {
		t.Fatal("缺失文件应恢复失败")
	}
	result := GetRunningResult()
	if result == nil || result.Type != "restore" || result.IsRunning || resultStatus(t, result) != "failed" || result.ErrorMsg == "" {
		t.Fatalf("恢复早退必须发布本轮失败终态，实际 %+v", result)
	}
}

func TestGetRunningResultReturnsIndependentSnapshot(t *testing.T) {
	setupBackupTest(t)
	SetRunningResult("backup", "正在备份", 2, 1, "")
	result := GetRunningResult()
	result.Desc = "被调用方篡改"
	if GetRunningResult().Desc != "正在备份" {
		t.Fatal("调用方不能修改共享进度")
	}
}

func TestBackupFileWriteFailureIsReturned(t *testing.T) {
	testDB := setupBackupTest(t)
	if _, err := os.Stat("/dev/full"); err != nil {
		t.Skip("需要 /dev/full 注入磁盘写入失败")
	}
	if err := testDB.Create(&backupTestItem{Name: "kept"}).Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink("/dev/full", filepath.Join(dir, "item.json")); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := backupToJsonFile(dir, "item", 1, &count, &backupTestItem{}); err == nil {
		t.Fatal("写入失败必须返回错误")
	}
	if count != 0 {
		t.Fatalf("失败表不能计为完成：%d", count)
	}
}

func TestRestoreMalformedRowIsReturned(t *testing.T) {
	setupBackupTest(t)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "item.json"), []byte("not-json\n"), 0600); err != nil {
		t.Fatal(err)
	}
	count := 0
	if err := restoreFromJsonFile(dir, "item", 1, &count, &backupTestItem{}); err == nil {
		t.Fatal("损坏记录必须让恢复失败")
	}
}

func TestBackupMetadataDownloadRoundTrip(t *testing.T) {
	testDB := setupBackupTest(t)
	models.AllTables = []any{models.DbDownloadTask{}}
	if err := testDB.AutoMigrate(&models.DbDownloadTask{}); err != nil {
		t.Fatal(err)
	}
	want := models.DbDownloadTask{ReplaceBaseline: "3:100:oldhash", PublishedSHA256: "newhash", LocalFullPath: "/tmp/metadata.nfo", LocalSourcePath: "/tmp/source.nfo", RemoteDownloadUrl: "https://example.invalid/file", DedupScopeHash: "scope", DedupLocatorHash: "locator", Status: models.DownloadStatusDownloading}
	if err := testDB.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	api, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(api), "oldhash") || strings.Contains(string(api), "newhash") {
		t.Fatal("private fields exposed")
	}
	if err := Backup(models.BackupTypeManual, "metadata recovery"); err != nil {
		t.Fatal(err)
	}
	var record models.BackupRecord
	if err := testDB.First(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := testDB.Delete(&want).Error; err != nil {
		t.Fatal(err)
	}
	if err := Restore(record.FilePath); err != nil {
		t.Fatal(err)
	}
	var got models.DbDownloadTask
	if err := testDB.First(&got).Error; err != nil {
		t.Fatal(err)
	}
	if got.ReplaceBaseline != want.ReplaceBaseline || got.PublishedSHA256 != want.PublishedSHA256 || got.LocalSourcePath != want.LocalSourcePath || got.RemoteDownloadUrl != want.RemoteDownloadUrl || got.DedupScopeHash != want.DedupScopeHash || got.DedupLocatorHash != want.DedupLocatorHash {
		t.Fatalf("lost recovery fields: %+v", got)
	}
}
