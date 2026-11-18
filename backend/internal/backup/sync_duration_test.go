package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestSyncDurationBackupCompatibility(t *testing.T) {
	testSyncDurationBackupCompatibility(t, setupBackupTest)
}

func testSyncDurationBackupCompatibility(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"legacy71", "logical71", "logical72"} {
		t.Run(format, func(t *testing.T) {
			conn := setup(t)
			models.AllTables = []any{&models.Migrator{}, &models.Sync{}}
			if err := conn.AutoMigrate(models.AllTables...); err != nil {
				t.Fatal(err)
			}
			if err := conn.Where("1 = 1").Delete(&models.Migrator{}).Error; err != nil {
				t.Fatal(err)
			}
			if err := conn.Create(&models.Migrator{VersionCode: models.MaxVersionCode}).Error; err != nil {
				t.Fatal(err)
			}
			net, local, generation, total := int64(123), int64(0), int64(234), int64(345)
			if err := conn.Create(&models.Sync{BaseModel: models.BaseModel{ID: 1, CreatedAt: 100}, Status: models.SyncStatusCompleted, FinishAt: 101,
				NetFileDurationMs: &net, LocalFileDurationMs: &local, GenerationDurationMs: &generation, TotalDurationMs: &total}).Error; err != nil {
				t.Fatal(err)
			}
			var archive string
			if format == "legacy71" {
				archive = writeBackupArchive(t, map[string]string{
					"Migrator.json": "{\"id\":1,\"version_code\":71}\n",
					"Sync.json":     "{\"id\":1,\"created_at\":100,\"finish_at\":101,\"status\":2}\n",
				}, zip.Deflate)
			} else {
				dir := t.TempDir()
				if err := writeLogicalBackup(conn, dir, nil); err != nil {
					t.Fatal(err)
				}
				if format == "logical71" {
					removeSyncDurationBackupFields(t, dir, conn)
				}
				archive = filepath.Join(t.TempDir(), "sync.zip")
				if err := helpers.ZipDir(dir, archive); err != nil {
					t.Fatal(err)
				}
			}
			if err := Restore(archive); err != nil {
				t.Fatal(err)
			}
			var got models.Sync
			if err := conn.First(&got, 1).Error; err != nil {
				t.Fatal(err)
			}
			if got.CreatedAt != 100 || got.FinishAt != 101 || got.Status != models.SyncStatusCompleted {
				t.Fatalf("恢复改写任务时间或结果：%+v", got)
			}
			if format == "logical72" {
				if got.NetFileDurationMs == nil || *got.NetFileDurationMs != 123 || got.LocalFileDurationMs == nil || *got.LocalFileDurationMs != 0 || got.GenerationDurationMs == nil || *got.GenerationDurationMs != 234 || got.TotalDurationMs == nil || *got.TotalDurationMs != 345 {
					t.Fatalf("备份丢失毫秒精度或零值：%+v", got)
				}
			} else if got.NetFileDurationMs != nil || got.LocalFileDurationMs != nil || got.GenerationDurationMs != nil || got.TotalDurationMs != nil {
				t.Fatalf("旧备份补造了毫秒精度：%+v", got)
			}
			var version models.Migrator
			if err := conn.First(&version).Error; err != nil || version.VersionCode != models.MaxVersionCode {
				t.Fatalf("恢复版本未更新：%+v %v", version, err)
			}
		})
	}
}

// 从当前导出独立移除 71 没有的列和数据，不使用生产兼容适配器构造夹具。
func removeSyncDurationBackupFields(t *testing.T, dir string, conn *gorm.DB) {
	t.Helper()
	manifest, err := prepareLogicalBackup(dir, conn)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 71
	fields := []string{"net_file_duration_ms", "local_file_duration_ms", "generation_duration_ms", "total_duration_ms"}
	for i := range manifest.Tables {
		table := &manifest.Tables[i]
		var content bytes.Buffer
		if err := readLogicalRows(dir, *table, func(row map[string]any) error {
			if table.Name == "migrator" {
				row["version_code"] = int64(71)
			}
			if table.Name == "syncs" {
				for _, field := range fields {
					delete(row, field)
				}
			}
			return json.NewEncoder(&content).Encode(row)
		}); err != nil {
			t.Fatal(err)
		}
		if table.Name == "syncs" {
			table.Columns = slices.DeleteFunc(table.Columns, func(column logicalColumn) bool { return slices.Contains(fields, column.Name) })
		}
		if err := os.WriteFile(filepath.Join(dir, table.File), content.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content.Bytes())
		table.SHA256 = hex.EncodeToString(sum[:])
	}
	writeCompatibilityManifest(t, dir, manifest)
}
