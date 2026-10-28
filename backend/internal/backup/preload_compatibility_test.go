package backup

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestorePreloadSchemaCompatibility(t *testing.T) {
	testRestorePreloadSchemaCompatibility(t, setupBackupTest)
}

func seedPreloadCompatibilityDatabase(t *testing.T, conn *gorm.DB) {
	t.Helper()
	models.AllTables = []any{&models.Settings{}, &models.Migrator{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := conn.Where("1 = 1").Delete(&models.Migrator{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Migrator{BaseModel: models.BaseModel{ID: 1}, VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Settings{BaseModel: models.BaseModel{ID: 1}, StrmPreloadEnabled: 1, LocalProxy: 1, MultiPlaybackEnabled: 1, StrmBaseUrl: "http://source.invalid"}).Error; err != nil {
		t.Fatal(err)
	}
}

func testRestorePreloadSchemaCompatibility(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"logical67", "logical68", "logical69", "logical70", "legacy66", "legacy67", "legacy68", "legacy69", "legacy70"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rollback_%t", format, fail), func(t *testing.T) {
				conn := setup(t)
				seedPreloadCompatibilityDatabase(t, conn)
				var version int
				if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimPrefix(format, "logical"), "legacy"), "%d", &version); err != nil {
					t.Fatal(err)
				}
				want := 0
				if version == 70 {
					want = 1
				}
				var archive string
				if strings.HasPrefix(format, "logical") {
					dir, _ := preloadCompatibilityBackupDirectory(t, conn, version)
					archive = filepath.Join(t.TempDir(), "preload.zip")
					if err := helpers.ZipDir(dir, archive); err != nil {
						t.Fatal(err)
					}
				} else {
					field := ""
					if version == 70 {
						field = `,"strm_preload_enabled":1`
					}
					archive = writeBackupArchive(t, map[string]string{
						"Migrator.json": fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", version),
						"Settings.json": `{"id":1,"local_proxy":1,"multi_playback_enabled":1,"strm_base_url":"http://source.invalid"` + field + "}\n",
					}, zip.Deflate)
				}
				target := 1 - want
				if err := conn.Model(&models.Settings{}).Where("id = ?", 1).Updates(map[string]any{"strm_preload_enabled": target, "local_proxy": 0, "strm_base_url": "http://target.invalid"}).Error; err != nil {
					t.Fatal(err)
				}
				failure := errors.New("test preload restore failure")
				if fail {
					if err := conn.Callback().Create().Before("gorm:create").Register("test:preload_restore", func(tx *gorm.DB) {
						if tx.Statement.Table == "settings" {
							tx.AddError(failure)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := Restore(archive)
				var stored models.Settings
				if readErr := conn.First(&stored).Error; readErr != nil {
					t.Fatal(readErr)
				}
				if fail {
					if !errors.Is(err, failure) || GetRunningResult().RestoreOutcome != "rolled_back" || stored.StrmPreloadEnabled != target || stored.StrmBaseUrl != "http://target.invalid" || stored.LocalProxy != 0 {
						t.Fatalf("失败恢复没有完整回滚：settings=%+v err=%v result=%+v", stored, err, GetRunningResult())
					}
				} else if err != nil || stored.StrmPreloadEnabled != want || stored.StrmBaseUrl != "http://source.invalid" || stored.LocalProxy != 1 || stored.MultiPlaybackEnabled != 1 {
					t.Fatalf("旧备份默认值 / 新备份往返不符：settings=%+v err=%v", stored, err)
				}
				var gotVersion models.Migrator
				if err := conn.First(&gotVersion).Error; err != nil || gotVersion.VersionCode != 70 {
					t.Fatalf("恢复版本不符：%+v %v", gotVersion, err)
				}
			})
		}
	}
}

// preloadCompatibilityBackupDirectory 独立生成旧 settings 清单及行，不调用生产兼容适配器。
func preloadCompatibilityBackupDirectory(t *testing.T, conn *gorm.DB, version int) (string, *logicalManifest) {
	t.Helper()
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, conn)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = version
	for i := range manifest.Tables {
		table := &manifest.Tables[i]
		var content bytes.Buffer
		if err := readLogicalRows(dir, *table, func(row map[string]any) error {
			if table.Name == "migrator" {
				row["version_code"] = int64(version)
			} else if table.Name == "settings" && version < 70 {
				delete(row, "strm_preload_enabled")
			}
			return json.NewEncoder(&content).Encode(row)
		}); err != nil {
			t.Fatal(err)
		}
		if table.Name == "settings" && version < 70 {
			table.Columns = slices.DeleteFunc(table.Columns, func(column logicalColumn) bool { return column.Name == "strm_preload_enabled" })
		}
		if err := os.WriteFile(filepath.Join(dir, table.File), content.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content.Bytes())
		table.SHA256 = hex.EncodeToString(sum[:])
	}
	writeCompatibilityManifest(t, dir, manifest)
	return dir, manifest
}

func TestPreloadCompatibilityRejectsUnknownShapes(t *testing.T) {
	for _, scenario := range []string{"unknown_version", "future_column", "missing_old_column", "new_schema_old_shape"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			seedPreloadCompatibilityDatabase(t, conn)
			dir, manifest := preloadCompatibilityBackupDirectory(t, conn, 69)
			switch scenario {
			case "unknown_version":
				manifest.SchemaVersion = 71
			case "new_schema_old_shape":
				manifest.SchemaVersion = 70
			default:
				for i := range manifest.Tables {
					if manifest.Tables[i].Name == "settings" {
						if scenario == "future_column" {
							manifest.Tables[i].Columns = append(manifest.Tables[i].Columns, logicalColumn{Name: "strm_preload_enabled", Type: "integer", Bits: 64})
						} else {
							manifest.Tables[i].Columns = manifest.Tables[i].Columns[1:]
						}
					}
				}
			}
			writeCompatibilityManifest(t, dir, manifest)
			if _, err := prepareRestorePlan(dir, conn); !errors.Is(err, ErrArchiveInvalid) && !errors.Is(err, ErrArchiveUnsupported) {
				t.Fatalf("未知清单被接受：%v", err)
			}
		})
	}
}
