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

func TestMediaLibraryRestoreSQLite(t *testing.T) {
	testMediaLibraryRestore(t, setupBackupTest)
}

func testMediaLibraryRestore(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"logical67", "logical68", "logical69", "logical70", "logical71", "legacy66", "legacy67", "legacy68", "legacy69", "legacy70", "legacy71"} {
		for _, rollback := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rollback_%t", format, rollback), func(t *testing.T) {
				conn := setup(t)
				models.AllTables = []any{&models.EmbyConfig{}, &models.JellyfinConfig{}, &models.Migrator{}}
				if err := conn.AutoMigrate(models.AllTables...); err != nil {
					t.Fatal(err)
				}
				for _, table := range []string{"emby_config", "jellyfin_config"} {
					urlColumn := strings.TrimSuffix(table, "_config") + "_url"
					if err := conn.Table(table).Create(map[string]any{"id": 1, "enabled": false, "enable_media_notification": 0,
						"sync_enabled": 0, urlColumn: "http://source.invalid"}).Error; err != nil {
						t.Fatal(err)
					}
				}
				version := 0
				if _, err := fmt.Sscanf(strings.TrimPrefix(strings.TrimPrefix(format, "logical"), "legacy"), "%d", &version); err != nil {
					t.Fatal(err)
				}
				archive := ""
				if strings.HasPrefix(format, "logical") {
					dir := mediaLibraryRestoreDirectory(t, conn, version)
					archive = filepath.Join(t.TempDir(), "media-library.zip")
					if err := helpers.ZipDir(dir, archive); err != nil {
						t.Fatal(err)
					}
				} else {
					fields := `{"id":1,"emby_url":"http://source.invalid","sync_enabled":0,"enable_media_notification":0`
					files := map[string]string{"Migrator.json": fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", version)}
					if version == 71 {
						fields += `,"enabled":false`
						files["JellyfinConfig.json"] = "{\"id\":1,\"enabled\":false,\"sync_enabled\":0,\"enable_media_notification\":0,\"jellyfin_url\":\"http://source.invalid\"}\n"
					}
					files["EmbyConfig.json"] = fields + "}\n"
					archive = writeBackupArchive(t, files, zip.Deflate)
				}
				for _, table := range []string{"emby_config", "jellyfin_config"} {
					urlColumn := strings.TrimSuffix(table, "_config") + "_url"
					if err := conn.Table(table).Where("id = 1").Updates(map[string]any{"enabled": true, "enable_media_notification": 1,
						urlColumn: "http://target.invalid"}).Error; err != nil {
						t.Fatal(err)
					}
				}
				failure := errors.New("media-library restore failure")
				if rollback {
					if err := conn.Callback().Create().Before("gorm:create").Register("test:media_library_restore", func(tx *gorm.DB) {
						if tx.Statement.Table == "jellyfin_config" {
							tx.AddError(failure)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := Restore(archive)
				var emby models.EmbyConfig
				var jellyfin models.JellyfinConfig
				if readErr := conn.First(&emby).Error; readErr != nil {
					t.Fatal(readErr)
				}
				if readErr := conn.First(&jellyfin).Error; readErr != nil {
					t.Fatal(readErr)
				}
				if rollback {
					if !errors.Is(err, failure) || GetRunningResult().RestoreOutcome != "rolled_back" ||
						!emby.Enabled || !jellyfin.Enabled || emby.EmbyUrl != "http://target.invalid" || jellyfin.EmbyUrl != "http://target.invalid" {
						t.Fatalf("restore did not roll back both configurations: %+v %+v %v", emby, jellyfin, err)
					}
				} else {
					wantEnabled, wantNotification := version < 71, 0
					if wantEnabled {
						wantNotification = 1
					}
					if err != nil || emby.Enabled != wantEnabled || emby.EnableMediaNotification != wantNotification ||
						emby.SyncEnabled != 0 || emby.EmbyUrl != "http://source.invalid" || jellyfin.Enabled || jellyfin.EnableMediaNotification != 0 || jellyfin.SyncEnabled != 0 {
						t.Fatalf("restore changed switch semantics: %+v %+v %v", emby, jellyfin, err)
					}
					if version < 71 && jellyfin.EmbyUrl != "" || version == 71 && jellyfin.EmbyUrl != "http://source.invalid" {
						t.Fatalf("Jellyfin inherited target or legacy connection: %+v", jellyfin)
					}
				}
			})
		}
	}
	for _, mediaOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("unselected configurations/media_%t", mediaOnly), func(t *testing.T) {
			conn := setup(t)
			models.AllTables = []any{&models.EmbyConfig{}, &models.JellyfinConfig{}, &models.Migrator{}, &models.EmbyMediaItem{}}
			if err := conn.AutoMigrate(models.AllTables...); err != nil {
				t.Fatal(err)
			}
			if err := conn.Model(&models.JellyfinConfig{}).Create(map[string]any{"id": 1, "enabled": true, "jellyfin_url": "http://target.invalid"}).Error; err != nil {
				t.Fatal(err)
			}
			files := map[string]string{"Migrator.json": "{\"id\":1,\"version_code\":70}\n"}
			if mediaOnly {
				files["EmbyMediaItem.json"] = "{\"id\":1,\"item_id\":\"101\",\"item_id_int\":101}\n"
			}
			archive := writeBackupArchive(t, files, zip.Deflate)
			if err := Restore(archive); err != nil {
				t.Fatal(err)
			}
			var jellyfin models.JellyfinConfig
			if err := conn.First(&jellyfin).Error; err != nil || !jellyfin.Enabled || jellyfin.EmbyUrl != "http://target.invalid" {
				t.Fatalf("partial restore changed unselected configuration: %+v %v", jellyfin, err)
			}
			if mediaOnly {
				var item models.EmbyMediaItem
				if err := conn.First(&item).Error; err != nil || item.MediaServerType != models.MediaServerEmby {
					t.Fatalf("old partial media did not inherit Emby: %+v %v", item, err)
				}
			}
		})
	}
}

// 从真实当前归档独立删去已知旧字段，不调用生产版本适配器。
func mediaLibraryRestoreDirectory(t *testing.T, conn *gorm.DB, version int) string {
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
	tables := make([]logicalTable, 0, len(manifest.Tables))
	for _, table := range manifest.Tables {
		if table.Name == "jellyfin_config" && version < 71 {
			if err := os.Remove(filepath.Join(dir, table.File)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		var content bytes.Buffer
		if err := readLogicalRows(dir, table, func(row map[string]any) error {
			if table.Name == "migrator" {
				row["version_code"] = int64(version)
			}
			if table.Name == "emby_config" {
				if version < 71 {
					delete(row, "enabled")
				}
				if version < 68 {
					delete(row, "path_mappings_json")
				}
			}
			return json.NewEncoder(&content).Encode(row)
		}); err != nil {
			t.Fatal(err)
		}
		if table.Name == "emby_config" {
			table.Columns = slices.DeleteFunc(table.Columns, func(column logicalColumn) bool {
				return column.Name == "enabled" && version < 71 || column.Name == "path_mappings_json" && version < 68
			})
		}
		if err := os.WriteFile(filepath.Join(dir, table.File), content.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content.Bytes())
		table.SHA256 = hex.EncodeToString(sum[:])
		tables = append(tables, table)
	}
	manifest.Tables = tables
	writeCompatibilityManifest(t, dir, manifest)
	return dir
}
