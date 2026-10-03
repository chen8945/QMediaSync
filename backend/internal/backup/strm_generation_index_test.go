package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestoreStrmGenerationQueueIndex(t *testing.T) {
	for _, failIndex := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "index_failure"}[failIndex], func(t *testing.T) {
			conn := setupBackupTest(t)
			models.AllTables = []any{&models.StrmGenerationTask{}}
			if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
				t.Fatal(err)
			}
			indexErr := errors.New("queue index creation failed")
			if failIndex {
				if err := conn.Callback().Raw().Before("gorm:raw").Register("test:queue_index", func(tx *gorm.DB) {
					if strings.Contains(tx.Statement.SQL.String(), "CREATE INDEX IF NOT EXISTS idx_strm_generation_tasks_queue") {
						tx.AddError(indexErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := Restore(writeBackupArchive(t, map[string]string{"StrmGenerationTask.json": "{\"id\":1,\"status\":\"pending\"}\n"}, zip.Deflate))
			if failIndex {
				if !errors.Is(err, indexErr) || GetRunningResult().Status != models.BackupStatusFailed {
					t.Fatalf("index failure must fail restore: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if conn.Migrator().HasIndex(&models.StrmGenerationTask{}, "idx_strm_generation_tasks_queue") == failIndex {
				t.Fatal("unexpected index state")
			}
			var task models.StrmGenerationTask
			if err := conn.First(&task, 1).Error; err != nil || task.Status != models.StrmGenerationStatusPending {
				t.Fatalf("restored task lost: %+v %v", task, err)
			}
		})
	}
}
