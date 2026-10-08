package backup

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestBackupRetentionRunsBeforeExport(t *testing.T) {
	for _, scenario := range []string{"success", "zip_failure", "panic", "archive_limit", "history_failure", "cleanup_failure", "cleanup_panic", "file_delete_failure", "query_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			service := models.GetBackupService()
			config := *service.GetBackupConfig()
			config.BackupRetention, config.BackupMaxCount = 1, 1
			if err := service.UpdateBackupConfig(&config); err != nil {
				t.Fatal(err)
			}
			backupDir := BackupDirectory()
			old := models.BackupRecord{
				BaseModel: models.BaseModel{CreatedAt: time.Now().Add(-48 * time.Hour).Unix()},
				Status:    models.BackupStatusCompleted, FilePath: filepath.Join(backupDir, "old.zip"),
			}
			failed := models.BackupRecord{Status: models.BackupStatusFailed, FilePath: filepath.Join(backupDir, "failed.zip")}
			for _, record := range []*models.BackupRecord{&old, &failed} {
				if err := conn.Create(record).Error; err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(record.FilePath, []byte("old archive"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			exportStarted := false
			originalZip := zipDir
			zipDir = func(source, destination string) error {
				exportStarted = true
				if _, err := os.Stat(old.FilePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("导出前应已清理旧文件：%v", err)
				}
				if err := conn.First(&models.BackupRecord{}, old.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("导出前应已清理旧记录：%v", err)
				}
				if scenario == "panic" {
					panic("zip interrupted")
				}
				if scenario == "zip_failure" {
					if err := os.WriteFile(destination, []byte("partial"), 0600); err != nil {
						t.Fatal(err)
					}
					return errors.New("zip failed")
				}
				if err := originalZip(source, destination); err != nil {
					return err
				}
				if scenario == "archive_limit" {
					return os.Truncate(destination, MaxArchiveSize+1)
				}
				return nil
			}
			if scenario == "history_failure" {
				if err := conn.Callback().Update().Before("gorm:update").Register("test:before_finish", func(tx *gorm.DB) {
					if record, ok := tx.Statement.Dest.(*models.BackupRecord); ok && record.Status == models.BackupStatusCompleted {
						tx.AddError(errors.New("history unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			cleanupFailed := scenario == "cleanup_failure" || scenario == "cleanup_panic" || scenario == "file_delete_failure" || scenario == "query_failure"
			if scenario == "cleanup_failure" || scenario == "cleanup_panic" {
				if err := conn.Callback().Delete().Before("gorm:begin_transaction").Register("test:cleanup_failure", func(tx *gorm.DB) {
					if scenario == "cleanup_panic" {
						panic("cleanup unavailable")
					}
					tx.AddError(errors.New("cleanup unavailable"))
				}); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "file_delete_failure" {
				if err := os.Remove(old.FilePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(old.FilePath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "query_failure" {
				if err := conn.Callback().Query().Before("gorm:query").Register("test:cleanup_query", func(tx *gorm.DB) {
					if tx.Statement.Table == "backup_record" {
						tx.AddError(errors.New("query unavailable"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := Backup(models.BackupTypeManual, scenario)
			if scenario == "query_failure" {
				conn.Callback().Query().Remove("test:cleanup_query")
			}
			if (err == nil) != (scenario == "success") {
				t.Fatalf("Backup() = %v，场景 %s", err, scenario)
			}
			if scenario == "archive_limit" && !errors.Is(err, ErrArchiveLimit) {
				t.Fatalf("丢失超限分类：%v", err)
			}
			if cleanupFailed && exportStarted {
				t.Fatal("清理失败后仍然导出了数据库")
			}
			if cleanupFailed {
				if err := conn.First(&models.BackupRecord{}, old.ID).Error; err != nil {
					t.Fatalf("清理失败应保留记录：%v", err)
				}
			} else {
				if _, err := os.Stat(old.FilePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("旧文件被恢复：%v", err)
				}
				if err := conn.First(&models.BackupRecord{}, old.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("旧记录被恢复：%v", err)
				}
			}
			if _, err := os.Stat(failed.FilePath); err != nil {
				t.Fatalf("不应清理失败记录对应文件：%v", err)
			}
			var latest models.BackupRecord
			if err := conn.Order("id DESC").First(&latest).Error; err != nil {
				t.Fatal(err)
			}
			wantStatus := models.BackupStatusFailed
			if scenario == "success" {
				wantStatus = models.BackupStatusCompleted
			}
			if latest.Status != wantStatus || GetRunningResult().Status != wantStatus {
				t.Fatalf("本轮结果不符：%+v %+v", latest, GetRunningResult())
			}
			for _, pattern := range []string{"backup-export-*", ".backup-publish-*.part"} {
				if files, _ := filepath.Glob(filepath.Join(backupDir, pattern)); len(files) != 0 {
					t.Fatalf("临时文件未清理：%v", files)
				}
			}
		})
	}
}

func TestBackupRetentionKeepsNewestRecordWhenCreatedInSameSecond(t *testing.T) {
	conn := setupBackupTest(t)
	service := models.GetBackupService()
	config := *service.GetBackupConfig()
	config.BackupMaxCount = 1
	if err := service.UpdateBackupConfig(&config); err != nil {
		t.Fatal(err)
	}
	old := models.BackupRecord{
		BaseModel: models.BaseModel{CreatedAt: time.Now().Unix()},
		Status:    models.BackupStatusCompleted, FilePath: filepath.Join(helpers.ConfigDir, "backups", "old.zip"),
	}
	if err := conn.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old.FilePath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := conn.Callback().Create().Before("gorm:create").Register("test:same_second", func(tx *gorm.DB) {
		if tx.Statement.Schema.Table == "backup_record" {
			tx.Statement.SetColumn("CreatedAt", old.CreatedAt)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := Backup(models.BackupTypeManual, "same second"); err != nil {
		t.Fatal(err)
	}
	var records []models.BackupRecord
	if err := conn.Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID <= old.ID || records[0].Status != models.BackupStatusCompleted {
		t.Fatalf("同秒备份应保留新记录：%+v", records)
	}
	if archive, err := zip.OpenReader(records[0].FilePath); err != nil {
		t.Fatalf("新备份文件被清理：%v", err)
	} else {
		archive.Close()
	}
}

func TestBackupAfterConfigDirectoryMove(t *testing.T) {
	for _, tc := range []struct {
		name      string
		retention int
		maxCount  int
	}{
		{name: "maximum_count", maxCount: 2},
		{name: "retention_days", retention: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := setupBackupTest(t)
			configureBackupRetention(t, 0, 0)
			original := backupTestItem{ID: 1, Name: "before config move"}
			if err := conn.Create(&original).Error; err != nil {
				t.Fatal(err)
			}
			var oldRecords []models.BackupRecord
			originalArchives := make(map[string][]byte)
			backupTypes := []string{models.BackupTypeManual, models.BackupTypeAuto}
			for i, backupType := range backupTypes {
				if err := Backup(backupType, "before config move"); err != nil {
					t.Fatal(err)
				}
				var record models.BackupRecord
				if err := conn.Order("id DESC").First(&record).Error; err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(record.FilePath)
				if err != nil {
					t.Fatal(err)
				}
				// 固定历史文件名，避免迁移前后的同类型备份在同秒发布时重名。
				name := "before-config-move-" + backupType + ".zip"
				oldPath := filepath.Join(BackupDirectory(), name)
				if err := os.Rename(record.FilePath, oldPath); err != nil {
					t.Fatal(err)
				}
				record.FilePath = oldPath
				record.CreatedAt = time.Now().Add(-time.Duration(i+2) * 24 * time.Hour).Unix()
				if err := conn.Save(&record).Error; err != nil {
					t.Fatal(err)
				}
				oldRecords = append(oldRecords, record)
				originalArchives[name] = content
			}
			configureBackupRetention(t, tc.retention, tc.maxCount)

			oldConfigDir, currentConfigDir := helpers.ConfigDir, t.TempDir()
			lock, err := helpers.AcquireInstanceLock(currentConfigDir)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := lock.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := helpers.MoveConfigDir(oldConfigDir, currentConfigDir); err != nil {
				t.Fatal(err)
			}
			helpers.ConfigDir = currentConfigDir
			models.GlobalBackupService = nil
			service := models.GetBackupService()
			for _, old := range oldRecords {
				var got models.BackupRecord
				if err := conn.First(&got, old.ID).Error; err != nil || got.FilePath != old.FilePath {
					t.Fatalf("迁移应保留历史中的旧路径：%+v，%v", got, err)
				}
				if _, err := os.Stat(old.FilePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("迁移后旧位置不应仍有备份：%v", err)
				}
			}
			if err := conn.Model(&original).Update("name", "after config move").Error; err != nil {
				t.Fatal(err)
			}

			// manual 和 auto 使用不同文件名，连续执行无需等待真实时钟。
			for _, backupType := range backupTypes {
				if err := Backup(backupType, "after config move"); err != nil {
					t.Fatalf("迁移后 %s 备份失败：%v", backupType, err)
				}
				if result := GetRunningResult(); result.Status != models.BackupStatusCompleted {
					t.Fatalf("迁移后 %s 备份未完成：%+v", backupType, result)
				}
				if tc.maxCount > 0 && backupType == models.BackupTypeManual {
					var kept models.BackupRecord
					if err := conn.First(&kept, oldRecords[0].ID).Error; err != nil || kept.FilePath != oldRecords[0].FilePath {
						t.Fatalf("首轮应保留一条旧路径记录供下一轮清理：%+v，%v", kept, err)
					}
					if err := conn.First(&models.BackupRecord{}, oldRecords[1].ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
						t.Fatalf("首轮应清理最旧的路径记录：%v", err)
					}
				}
			}
			for _, old := range oldRecords {
				if err := conn.First(&models.BackupRecord{}, old.ID).Error; !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("保留策略应清理旧路径记录：%v", err)
				}
			}
			records, total, err := service.GetBackupRecords(1, 10, "all")
			if err != nil || total != 2 || len(records) != 2 {
				t.Fatalf("应保留两次新备份历史：%+v，total=%d，err=%v", records, total, err)
			}
			for _, backupType := range backupTypes {
				found := false
				for _, record := range records {
					if record.BackupType != backupType {
						continue
					}
					found = true
					if record.Status != models.BackupStatusCompleted || filepath.Dir(record.FilePath) != BackupDirectory() || record.FileSize <= 0 {
						t.Fatalf("新备份历史不完整：%+v", record)
					}
					archive, err := zip.OpenReader(record.FilePath)
					if err != nil {
						t.Fatalf("新备份 ZIP 不可用：%v", err)
					}
					if err := archive.Close(); err != nil {
						t.Fatal(err)
					}
				}
				if !found {
					t.Fatalf("缺少迁移后的 %s 备份历史", backupType)
				}
			}

			files, err := ListBackupFiles()
			if err != nil {
				t.Fatal(err)
			}
			var restorePath string
			for _, file := range files {
				want, ok := originalArchives[file.FileName]
				if !ok {
					continue
				}
				path, err := ResolveBackupFile(file.FileName)
				if err != nil {
					t.Fatal(err)
				}
				content, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(content, want) {
					t.Fatalf("迁入的旧 ZIP 被删除或改写：%s，%v", file.FileName, err)
				}
				restorePath = path
				delete(originalArchives, file.FileName)
			}
			if len(originalArchives) != 0 {
				t.Fatalf("本地文件列表缺少迁入的旧 ZIP：%d", len(originalArchives))
			}
			if err := Restore(restorePath); err != nil {
				t.Fatalf("从本地文件列表选择的迁入 ZIP 恢复失败：%v", err)
			}
			var restored backupTestItem
			if err := conn.First(&restored, original.ID).Error; err != nil || restored.Name != "before config move" {
				t.Fatalf("迁入 ZIP 未恢复原数据：%+v，%v", restored, err)
			}
		})
	}
}
