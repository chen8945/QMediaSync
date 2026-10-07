package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestBackupRetentionWaitsForSuccessfulPublicationAndHistory(t *testing.T) {
	for _, scenario := range []string{"success", "zip_failure", "panic", "archive_limit", "history_failure", "cleanup_failure", "cleanup_panic"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			service := models.GetBackupService()
			config := *service.GetBackupConfig()
			config.BackupRetention, config.BackupMaxCount = 1, 1
			if err := service.UpdateBackupConfig(&config); err != nil {
				t.Fatal(err)
			}
			backupDir := filepath.Join(helpers.ConfigDir, "backups")
			old := models.BackupRecord{
				BaseModel: models.BaseModel{CreatedAt: time.Now().Add(-48 * time.Hour).Unix()},
				Status:    models.BackupStatusCompleted, FilePath: filepath.Join(backupDir, "old.zip"),
			}
			failed := models.BackupRecord{Status: models.BackupStatusFailed, FilePath: filepath.Join(backupDir, "failed.zip")}
			for _, record := range []*models.BackupRecord{&old, &failed} {
				if err := conn.Create(record).Error; err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(record.FilePath, []byte("keep until success"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			originalZip := zipDir
			zipDir = func(source, destination string) error {
				if _, err := os.Stat(old.FilePath); err != nil {
					t.Fatalf("备份发布前旧文件已丢失：%v", err)
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
				if files, _ := filepath.Glob(filepath.Join(backupDir, "backup_manual_*.zip")); len(files) != 0 {
					t.Fatalf("发布前不能出现最终文件：%v", files)
				}
				if scenario == "archive_limit" {
					return os.Truncate(destination, MaxArchiveSize+1)
				}
				return nil
			}
			if err := conn.Callback().Update().Before("gorm:update").Register("test:before_finish", func(tx *gorm.DB) {
				record, ok := tx.Statement.Dest.(*models.BackupRecord)
				if !ok || record.Status != models.BackupStatusCompleted {
					return
				}
				if _, err := os.Stat(old.FilePath); err != nil {
					t.Fatalf("完成记录保存前旧文件已丢失：%v", err)
				}
				archive, err := zip.OpenReader(record.FilePath)
				if err != nil {
					t.Fatalf("完成记录保存前必须有完整归档：%v", err)
				}
				archive.Close()
				if scenario == "history_failure" {
					tx.AddError(errors.New("history unavailable"))
				}
			}); err != nil {
				t.Fatal(err)
			}
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
			err := Backup(models.BackupTypeManual, scenario)
			success := scenario == "success" || scenario == "cleanup_failure" || scenario == "cleanup_panic"
			if (err == nil) != success {
				t.Fatalf("Backup() = %v，期望成功=%v", err, success)
			}
			if scenario == "archive_limit" && !errors.Is(err, ErrArchiveLimit) {
				t.Fatalf("大小限制丢失错误分类：%v", err)
			}
			if !success {
				if _, err := os.Stat(old.FilePath); err != nil {
					t.Fatalf("失败后必须保留旧备份：%v", err)
				}
				if err := conn.First(&models.BackupRecord{}, old.ID).Error; err != nil {
					t.Fatalf("失败后必须保留旧记录：%v", err)
				}
				if scenario != "history_failure" {
					if files, _ := filepath.Glob(filepath.Join(backupDir, "backup_manual_*.zip")); len(files) != 0 {
						t.Fatalf("失败或超限归档不得发布：%v", files)
					}
				}
			} else if GetRunningResult().Status != models.BackupStatusCompleted {
				t.Fatalf("清理不应抹掉成功状态：%+v", GetRunningResult())
			}
			if scenario == "success" {
				if _, err := os.Stat(old.FilePath); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("成功后应清理过期备份：%v", err)
				}
			}
			if _, err := os.Stat(failed.FilePath); err != nil {
				t.Fatalf("失败记录的保留策略不能改变：%v", err)
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
