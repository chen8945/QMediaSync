package backup

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/emby"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/synccron"
)

// 使用真实备份／恢复和任务启停流程，仅替换 Webhook worker 的生命周期边界。
func setupBackupWebhookTest(t *testing.T) *gorm.DB {
	t.Helper()
	testDB := setupBackupTest(t)
	originalStop, originalStart := stopWebhookWorker, startWebhookWorker
	originalGlobal, originalSync, originalScrape := synccron.GlobalCron, synccron.SyncCron, synccron.ScrapeCron
	originalSettings, originalEmby := models.SettingsGlobal, models.GlobalEmbyConfig
	originalBusy := emby.IsEmbySyncRunning()
	models.SettingsGlobal, models.GlobalEmbyConfig = &models.Settings{}, &models.EmbyConfig{}
	synccron.GlobalCron, synccron.SyncCron, synccron.ScrapeCron = nil, nil, nil
	pauseTasks, resumeTasks = stopAllTasks, startAllTasks
	emby.SetEmbySyncRunning(false)
	stopWebhookWorker = func(context.Context) error { return nil }
	startWebhookWorker = func() error { return nil }
	t.Cleanup(func() {
		if synccron.GlobalCron != nil {
			<-synccron.GlobalCron.Stop().Done()
		}
		if synccron.SyncCron != nil {
			<-synccron.SyncCron.Stop().Done()
		}
		if synccron.ScrapeCron != nil {
			<-synccron.ScrapeCron.Stop().Done()
		}
		synccron.GlobalCron, synccron.SyncCron, synccron.ScrapeCron = originalGlobal, originalSync, originalScrape
		models.SettingsGlobal, models.GlobalEmbyConfig = originalSettings, originalEmby
		emby.SetEmbySyncRunning(originalBusy)
		stopWebhookWorker, startWebhookWorker = originalStop, originalStart
	})
	if err := testDB.AutoMigrate(&models.SyncPath{}, &models.ScrapePath{}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := testDB.Create(&backupTestItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	return testDB
}

func backupWebhookOperation(t *testing.T, operation string) func() error {
	t.Helper()
	if operation == "backup" {
		return func() error { return Backup(models.BackupTypeManual, "webhook lifecycle") }
	}
	archive := writeBackupArchive(t, map[string]string{
		"backupTestItem.json": "{\"ID\":1,\"Name\":\"restored\"}\n",
	}, zip.Deflate)
	return func() error { return Restore(archive) }
}

func TestBackupRestoreWebhookStopFailurePreservesData(t *testing.T) {
	for _, operation := range []string{"backup", "restore"} {
		t.Run(operation, func(t *testing.T) {
			testDB := setupBackupWebhookTest(t)
			run := backupWebhookOperation(t, operation)
			stopErr := errors.New("webhook did not stop")
			stopCalls, startCalls, exports := 0, 0, 0
			stopWebhookWorker = func(ctx context.Context) error {
				stopCalls++
				if _, ok := ctx.Deadline(); !ok {
					t.Error("后台停止缺少等待期限")
				}
				return stopErr
			}
			startWebhookWorker = func() error { startCalls++; return nil }
			if err := testDB.Callback().Query().Before("gorm:query").Register("test:business_export", func(tx *gorm.DB) {
				if tx.Statement.Table == "backup_test_items" {
					exports++
				}
			}); err != nil {
				t.Fatal(err)
			}
			err := run()
			if !errors.Is(err, stopErr) || stopCalls != 1 || startCalls != 0 || exports != 0 {
				t.Fatalf("err=%v stop=%d start=%d exports=%d", err, stopCalls, startCalls, exports)
			}
			if emby.IsEmbySyncRunning() {
				t.Fatal("停止 Webhook 失败不应修改普通同步状态")
			}
			var got backupTestItem
			if err := testDB.First(&got).Error; err != nil || got.Name != "before" {
				t.Fatalf("停止失败后业务表发生变化：%+v %v", got, err)
			}
			if result := GetRunningResult(); result.Status != models.BackupStatusFailed || result.IsRunning {
				t.Fatalf("停止失败应明确报告失败：%+v", result)
			}
		})
	}
}

func TestBackupRestoreWaitForWebhookBeforeTables(t *testing.T) {
	for _, operation := range []string{"backup", "restore"} {
		t.Run(operation, func(t *testing.T) {
			testDB := setupBackupWebhookTest(t)
			run := backupWebhookOperation(t, operation)
			stopping, allowStop, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
			var stopped atomic.Bool
			startCalls := 0
			stopWebhookWorker = func(ctx context.Context) error {
				close(stopping)
				select {
				case <-allowStop:
					stopped.Store(true)
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			startWebhookWorker = func() error {
				startCalls++
				if !stopped.Load() {
					return errors.New("previous worker still running")
				}
				var got backupTestItem
				if err := testDB.First(&got).Error; err != nil {
					return err
				}
				if operation == "restore" && got.Name != "restored" {
					return errors.New("worker restarted before table restore completed")
				}
				return nil
			}
			go func() { finished <- run() }()
			<-stopping
			var got backupTestItem
			readErr := testDB.First(&got).Error
			close(allowStop)
			if err := <-finished; err != nil {
				t.Fatal(err)
			}
			if readErr != nil || got.Name != "before" || startCalls != 1 {
				t.Fatalf("等待退出时发生表替换，或未恢复：item=%+v err=%v starts=%d", got, readErr, startCalls)
			}
			if result := GetRunningResult(); result.Status != models.BackupStatusCompleted {
				t.Fatalf("成功任务终态错误：%+v", result)
			}
		})
	}
}

func TestBackupRestoreWebhookRestartsAfterFailure(t *testing.T) {
	for _, operation := range []string{"backup", "restore"} {
		for _, restartFails := range []bool{false, true} {
			name := operation + "/restart_ok"
			if restartFails {
				name = operation + "/restart_failed"
			}
			t.Run(name, func(t *testing.T) {
				setupBackupWebhookTest(t)
				run := backupWebhookOperation(t, operation)
				operationErr, restartErr := errors.New("archive failed"), errors.New("worker recovery failed")
				if operation == "backup" {
					zipDir = func(string, string) error { return operationErr }
				} else {
					archive := filepath.Join(helpers.ConfigDir, "broken.zip")
					if err := os.WriteFile(archive, []byte("invalid zip"), 0600); err != nil {
						t.Fatal(err)
					}
					run = func() error { return Restore(archive) }
				}
				starts, stops := 0, 0
				stopWebhookWorker = func(context.Context) error { stops++; return nil }
				startWebhookWorker = func() error {
					starts++
					if restartFails {
						return restartErr
					}
					return nil
				}
				err := run()
				if err == nil || starts != 1 || stops != 1 || errors.Is(err, restartErr) != restartFails {
					t.Fatalf("err=%v starts=%d stops=%d", err, starts, stops)
				}
				if operation == "backup" && !errors.Is(err, operationErr) {
					t.Fatalf("后台恢复覆盖了原始备份错误：%v", err)
				}
				if result := GetRunningResult(); result.Status != models.BackupStatusFailed {
					t.Fatalf("失败被报告为成功：%+v", result)
				}
			})
		}
	}
}

func TestBackupRestoreWebhookRestartFailureVisible(t *testing.T) {
	for _, operation := range []string{"backup", "restore"} {
		t.Run(operation, func(t *testing.T) {
			setupBackupWebhookTest(t)
			run := backupWebhookOperation(t, operation)
			restartErr := errors.New("worker recovery failed")
			startWebhookWorker = func() error { return restartErr }
			if err := run(); !errors.Is(err, restartErr) {
				t.Fatalf("后台恢复失败未传播：%v", err)
			}
			if result := GetRunningResult(); result.Status != models.BackupStatusFailed {
				t.Fatalf("后台未恢复仍报告成功：%+v", result)
			}
		})
	}
}
