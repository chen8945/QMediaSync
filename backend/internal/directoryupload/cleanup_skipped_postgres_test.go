//go:build integration

package directoryupload

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/models"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCleanupSkippedPostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	schema := fmt.Sprintf("qms_d_cleanup_%d", time.Now().UnixNano())
	if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	oldDB := db.Db
	t.Cleanup(func() { db.Db = oldDB; conn.Exec("DROP SCHEMA " + schema + " CASCADE"); sqlDB.Close() })
	if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	db.Db = conn
	if err := conn.AutoMigrate(&models.Account{}, &models.SyncPath{}, &models.DirectoryUploadRule{}, &models.DirectoryUploadProcessedFile{}, &models.DbUploadTask{}, &models.StrmGenerationTask{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Account{BaseModel: models.BaseModel{ID: 1}, SourceType: models.SourceType115}).Error; err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, root)
	rule.DeleteSourceAfterSuccess = true
	if err := conn.Save(rule).Error; err != nil {
		t.Fatal(err)
	}
	skippedPath := filepath.Join(root, "skipped.mkv")
	writeFileWithMtime(t, skippedPath, []byte("skipped"), time.Now())
	skippedUpload := createCleanupUploadTask(t, rule, skippedPath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, skippedUpload.ID, models.StrmGenerationStatusCompleted)
	createCleanupStrmTask(t, skippedUpload.ID, "skipped")
	successPath := filepath.Join(root, "success.mkv")
	writeFileWithMtime(t, successPath, []byte("success"), time.Now())
	successUpload := createCleanupUploadTask(t, rule, successPath, models.UploadResultMultipartUploaded)
	createCleanupStrmTask(t, successUpload.ID, models.StrmGenerationStatusCompleted)
	if err := CleanupSourceAfterStrmSuccess(skippedUpload.ID); err != nil {
		t.Fatal(err)
	}
	assertPathExists(t, skippedPath)
	candidates, err := findCompletedStrmDependencyUploadTasks(0, 1)
	if err != nil || len(candidates) != 1 || candidates[0].ID != successUpload.ID {
		t.Fatalf("candidates=%+v err=%v", candidates, err)
	}
	cleaned, err := CleanupCompletedStrmDependencies(1)
	if err != nil || cleaned != 1 {
		t.Fatalf("cleaned=%d err=%v", cleaned, err)
	}
	assertPathExists(t, skippedPath)
	assertPathMissing(t, successPath)
	// 新任务真正成功后，旧的跳过记录不妨碍清理。
	createCleanupStrmTask(t, skippedUpload.ID, models.StrmGenerationStatusCompleted)
	cleaned, err = CleanupCompletedStrmDependencies(1)
	if err != nil || cleaned != 1 {
		t.Fatalf("retry cleaned=%d err=%v", cleaned, err)
	}
	assertPathMissing(t, skippedPath)
}
