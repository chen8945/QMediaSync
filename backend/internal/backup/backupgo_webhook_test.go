package backup

import (
	"archive/zip"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
)

func setupBackupWebhookTest(t *testing.T) *gorm.DB {
	t.Helper()
	database := setupBackupTest(t)
	if err := database.Create(&backupTestItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	return database
}

func setupRestoreMaintenanceTest(t *testing.T) (*gorm.DB, *gorm.DB) {
	t.Helper()
	setupBackupTest(t)
	file := filepath.Join(t.TempDir(), "restore.db")
	database := db.InitSqlite3(file)
	db.Db = database
	if err := database.AutoMigrate(&backupTestItem{}, &models.UserSession{}); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&backupTestItem{ID: 1, Name: "before"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&models.UserSession{SessionID: "browser", TokenID: "token", UserID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	observer, err := gorm.Open(sqlite.Open(file), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	oldLifecycle := realtime.GlobalLifecycle
	realtime.GlobalLifecycle = realtime.NewLifecycle()
	oldStop, oldStart := stopWebhookWorker, startWebhookWorker
	stopWebhookWorker = func(context.Context) error { return nil }
	startWebhookWorker = func() error { t.Error("maintenance must not restart old workers"); return nil }
	pauseTasks = func() error {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		return stopWebhookWorker(ctx)
	}
	beginRestoreMaintenance = beginRestoreMaintenanceRuntime
	t.Cleanup(func() {
		pool, _ := database.DB()
		_ = pool.Close()
		pool, _ = observer.DB()
		_ = pool.Close()
		realtime.GlobalLifecycle = oldLifecycle
		stopWebhookWorker, startWebhookWorker = oldStop, oldStart
		restoreRequests.Lock()
		restoreRequests.blocked = false
		restoreRequests.Unlock()
	})
	return database, observer
}

func restoreMaintenanceArchive(t *testing.T) string {
	t.Helper()
	return writeBackupArchive(t, map[string]string{"backupTestItem.json": `{"ID":1,"Name":"restored"}` + "\n"}, zip.Deflate)
}

func TestRestoreMaintenancePreflightFailureDoesNotStopServices(t *testing.T) {
	database, _ := setupRestoreMaintenanceTest(t)
	stopWebhookWorker = func(context.Context) error { t.Error("invalid archive stopped background work"); return nil }
	file := filepath.Join(helpers.ConfigDir, "invalid.zip")
	if err := os.WriteFile(file, []byte("invalid zip"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Restore(file); err == nil {
		t.Fatal("invalid archive was accepted")
	}
	if db.IsMaintenance(database) || GetRunningResult().RestartRequired {
		t.Fatal("preflight failure entered maintenance")
	}
	if err := database.Exec("UPDATE backup_test_items SET name='available'").Error; err != nil {
		t.Fatal(err)
	}
}

func TestRestoreMaintenanceKeepsGateAfterSuccessOrRollback(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "rolled_back"}[fail], func(t *testing.T) {
			database, observer := setupRestoreMaintenanceTest(t)
			if fail {
				if err := database.Callback().Create().Before("gorm:create").Register("test:restore_failure", func(tx *gorm.DB) { tx.AddError(errors.New("injected write failure")) }); err != nil {
					t.Fatal(err)
				}
			}
			if err := Restore(restoreMaintenanceArchive(t)); (err != nil) != fail {
				t.Fatalf("restore err=%v", err)
			}
			result := GetRunningResult()
			expected := models.BackupStatusCompleted
			expectedName := "restored"
			expectedSessions := int64(0)
			if fail {
				expected = models.BackupStatusFailed
				expectedName = "before"
				expectedSessions = 1
			}
			if result.Status != expected || !result.RestartRequired || result.IsRunning {
				t.Fatalf("result=%+v", result)
			}
			if err := database.Exec("UPDATE backup_test_items SET name='stale worker'").Error; !errors.Is(err, db.ErrMaintenance) {
				t.Fatalf("stale write accepted: %v", err)
			}
			if release, ok := AcquireRuntimeRequest(); ok {
				release()
				t.Fatal("request admission reopened")
			}
			var item backupTestItem
			if err := observer.First(&item).Error; err != nil || item.Name != expectedName {
				t.Fatalf("item=%+v err=%v", item, err)
			}
			var count int64
			if err := observer.Model(&models.UserSession{}).Count(&count).Error; err != nil || count != expectedSessions {
				t.Fatalf("sessions=%d err=%v", count, err)
			}
		})
	}
}

func TestRestoreMaintenanceWaitsForWebhookBeforeReplacingTables(t *testing.T) {
	database, observer := setupRestoreMaintenanceTest(t)
	stopping, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	stopWebhookWorker = func(ctx context.Context) error {
		close(stopping)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	archive := restoreMaintenanceArchive(t)
	go func() { finished <- Restore(archive) }()
	<-stopping
	var item backupTestItem
	readErr := observer.First(&item).Error
	staleErr := database.Exec("UPDATE backup_test_items SET name='stale'").Error
	close(release)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	if readErr != nil || item.Name != "before" || !errors.Is(staleErr, db.ErrMaintenance) {
		t.Fatalf("item=%+v read=%v stale=%v", item, readErr, staleErr)
	}
}

func TestRestoreMaintenanceStopFailurePreservesDataAndRequiresRestart(t *testing.T) {
	database, observer := setupRestoreMaintenanceTest(t)
	stopError := errors.New("webhook did not stop")
	stopWebhookWorker = func(context.Context) error { return stopError }
	if err := Restore(restoreMaintenanceArchive(t)); !errors.Is(err, stopError) {
		t.Fatalf("error=%v", err)
	}
	if !db.IsMaintenance(database) || !GetRunningResult().RestartRequired {
		t.Fatal("failed stop reopened maintenance")
	}
	var item backupTestItem
	if err := observer.First(&item).Error; err != nil || item.Name != "before" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
}

func TestBackupSnapshotDoesNotStopBackgroundWorkers(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "archive_failure"}[fail], func(t *testing.T) {
			setupBackupWebhookTest(t)
			pauseTasks = func() error { t.Error("snapshot backup must not pause background work"); return nil }
			resumeTasks = func() error { t.Error("snapshot backup must not restart background work"); return nil }
			if fail {
				zipDir = func(string, string) error { return errors.New("archive failed") }
			}
			err := Backup(models.BackupTypeManual, "snapshot lifecycle")
			if (err != nil) != fail {
				t.Fatalf("backup error=%v, want failure=%v", err, fail)
			}
		})
	}
}

func TestRestoreMaintenanceWaitsForAcceptedRequests(t *testing.T) {
	database, observer := setupRestoreMaintenanceTest(t)
	release, ok := AcquireRuntimeRequest()
	if !ok {
		t.Fatal("request was unexpectedly rejected")
	}
	defer release()
	archive := restoreMaintenanceArchive(t)
	finished := make(chan error, 1)
	go func() { finished <- Restore(archive) }()
	deadline := time.After(5 * time.Second)
	for !db.IsMaintenance(database) {
		select {
		case <-deadline:
			t.Fatal("maintenance did not close SQL")
		case <-time.After(time.Millisecond):
		}
	}
	select {
	case <-finished:
		t.Fatal("restore replaced tables before accepted request finished")
	default:
	}
	var item backupTestItem
	if err := observer.First(&item).Error; err != nil || item.Name != "before" {
		t.Fatalf("item=%+v err=%v", item, err)
	}
	release()
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}

func TestRestoreMaintenanceReceiptSurvivesSessionRemoval(t *testing.T) {
	database, observer := setupRestoreMaintenanceTest(t)
	receipt, err := StartRestoreWithReceipt(restoreMaintenanceArchive(t), false)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(5 * time.Second)
	for IsRunning() {
		select {
		case <-deadline:
			t.Fatal("restore did not finish")
		case <-time.After(time.Millisecond):
		}
	}
	result, ok := RestoreResultWithReceipt(receipt)
	if !ok || result.Status != models.BackupStatusCompleted || !result.RestartRequired {
		t.Fatalf("receipt result=%+v ok=%t", result, ok)
	}
	if _, ok := RestoreResultWithReceipt(receipt + "invalid"); ok {
		t.Fatal("invalid receipt was accepted")
	}
	var count int64
	if err := observer.Model(&models.UserSession{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("sessions=%d err=%v", count, err)
	}
	if err := database.Model(&models.UserSession{}).Count(&count).Error; !errors.Is(err, db.ErrMaintenance) {
		t.Fatalf("ordinary authentication query passed gate: %v", err)
	}
}
