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

const restoredGUID = "112233445566778899aabbccddeeff00"
const restoredLargeID = int64(9007199254740993)

func TestRestoreEmbyIdentitySchemaCompatibility(t *testing.T) {
	testRestoreEmbyIdentitySchemaCompatibility(t, setupBackupTest)
}

func testRestoreEmbyIdentitySchemaCompatibility(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"logical", "legacy"} {
		firstVersion := 67
		if format == "legacy" {
			firstVersion = 66
		}
		for version := firstVersion; version <= 71; version++ {
			for _, rollback := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s%d/rollback_%t", format, version, rollback), func(t *testing.T) {
					conn := setup(t)
					seedEmbyIdentityRestoreDatabase(t, conn)
					dir := embyIdentityBackupDirectory(t, conn, format, version)
					archive := filepath.Join(t.TempDir(), "identities.zip")
					if err := helpers.ZipDir(dir, archive); err != nil {
						t.Fatal(err)
					}
					if err := conn.Model(&models.EmbyMediaItem{}).Where("id = ?", 1).UpdateColumn("item_id", "target-item").Error; err != nil {
						t.Fatal(err)
					}
					failure := errors.New("injected identity restore failure")
					if rollback {
						if err := conn.Callback().Create().Before("gorm:create").Register("test:identity_restore", func(tx *gorm.DB) {
							if tx.Statement.Table == "emby_media_sync_files" {
								tx.AddError(failure)
							}
						}); err != nil {
							t.Fatal(err)
						}
					}
					err := Restore(archive)
					var item models.EmbyMediaItem
					if readErr := conn.First(&item, 1).Error; readErr != nil {
						t.Fatal(readErr)
					}
					if rollback {
						if !errors.Is(err, failure) || GetRunningResult().RestoreOutcome != "rolled_back" || item.ItemId != "target-item" {
							t.Fatalf("failed identity import changed target data: item=%s err=%v outcome=%s", item.ItemId, err, GetRunningResult().RestoreOutcome)
						}
					} else if err != nil || item.ItemId != "00042" {
						t.Fatalf("identity restore failed: item=%s err=%v", item.ItemId, err)
					}
					assertRestoredEmbyIdentities(t, conn)
					var storedVersion models.Migrator
					if err := conn.First(&storedVersion).Error; err != nil || storedVersion.VersionCode != models.MaxVersionCode {
						t.Fatalf("schema version was not finalized: %+v %v", storedVersion, err)
					}
				})
			}
		}
	}
}

func seedEmbyIdentityRestoreDatabase(t *testing.T, conn *gorm.DB) {
	t.Helper()
	models.AllTables = []any{&models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.Migrator{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := conn.Where("1 = 1").Delete(&models.Migrator{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, row := range []any{
		&models.Migrator{BaseModel: models.BaseModel{ID: 1}, VersionCode: models.MaxVersionCode},
		&models.EmbyMediaItem{BaseModel: models.BaseModel{ID: 1}, ItemId: "00042", ItemIdInt: 42},
		&models.EmbyMediaItem{BaseModel: models.BaseModel{ID: 2}, ItemId: restoredGUID, ItemIdInt: restoredLargeID},
		&models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: 1}, EmbyItemId: "00042", SyncFileId: 12},
		&models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: 2}, EmbyItemId: restoredGUID, SyncFileId: 13},
		&models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: 3}, EmbyItemId: "99", SyncFileId: 14},
		&models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: 4}, SyncFileId: 15},
		&models.EmbyMediaSyncFile{BaseModel: models.BaseModel{ID: 5}, EmbyItemId: "77", SyncFileId: 16},
	} {
		if err := conn.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Model(&models.EmbyMediaSyncFile{}).Where("id = ?", 4).UpdateColumn("emby_item_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	// NULL 身份不能遮盖同编号的有效身份，也不能把孤儿编号改成空字符串。
	for i, id := range []int64{42, 77} {
		if err := conn.Table("emby_media_items").Create(map[string]any{"id": i + 3, "item_id": nil, "item_id_int": id}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertRestoredEmbyIdentities(t *testing.T, conn *gorm.DB) {
	t.Helper()
	var links []struct {
		ID         uint
		EmbyItemID *string
		SyncFileID uint
	}
	if err := conn.Table("emby_media_sync_files").Order("id").Find(&links).Error; err != nil {
		t.Fatal(err)
	}
	want := []string{"00042", restoredGUID, "99", "", "77"}
	if len(links) != len(want) {
		t.Fatalf("wrong restored link count: %d", len(links))
	}
	for i, link := range links {
		if link.ID != uint(i+1) || link.SyncFileID != uint(i+12) {
			t.Fatalf("link identity or file reference changed: %+v", link)
		}
		if i == 3 {
			if link.EmbyItemID != nil {
				t.Fatal("NULL link identity was replaced")
			}
		} else if link.EmbyItemID == nil || *link.EmbyItemID != want[i] {
			t.Fatalf("link %d did not preserve source identity: %+v", link.ID, link.EmbyItemID)
		}
	}
	if !conn.Migrator().HasIndex(&models.EmbyMediaSyncFile{}, "idx_emby_media_item_id") {
		t.Fatal("restored identity lookup index is missing")
	}
}

// 独立生成历史整数清单与旧模型 JSON，避免使用生产适配器构造测试输入。
func embyIdentityBackupDirectory(t *testing.T, conn *gorm.DB, format string, version int) string {
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
			if version < 71 {
				delete(row, "media_server_type")
			}
			if table.Name == "migrator" {
				row["version_code"] = int64(version)
			} else if table.Name == "emby_media_sync_files" && version < 71 {
				row["emby_item_id"] = []any{int64(42), restoredLargeID, int64(99), nil, int64(77)}[row["id"].(int64)-1]
			}
			return json.NewEncoder(&content).Encode(row)
		}); err != nil {
			t.Fatal(err)
		}
		if version < 71 {
			table.Columns = slices.DeleteFunc(table.Columns, func(column logicalColumn) bool { return column.Name == "media_server_type" })
		}
		if table.Name == "emby_media_sync_files" && version < 71 {
			for j := range table.Columns {
				if table.Columns[j].Name == "emby_item_id" {
					table.Columns[j] = logicalColumn{Name: "emby_item_id", Type: "unsigned_integer", Bits: 64}
				}
			}
		}
		path := filepath.Join(dir, table.File)
		if format == "legacy" {
			path = filepath.Join(dir, table.ModelName+".json")
		}
		if err := os.WriteFile(path, content.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(content.Bytes())
		table.SHA256 = hex.EncodeToString(sum[:])
	}
	if format == "legacy" {
		if err := os.Remove(filepath.Join(dir, logicalManifestFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "tables")); err != nil {
			t.Fatal(err)
		}
	} else {
		writeCompatibilityManifest(t, dir, manifest)
	}
	return dir
}

func TestRestoreLegacyEmbyIdentityPreflight(t *testing.T) {
	testRestoreLegacyEmbyIdentityPreflight(t, setupBackupTest)
}

func testRestoreLegacyEmbyIdentityPreflight(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, scenario := range []string{"conflict", "missing_media", "null_link", "negative", "fraction", "overflow", "text", "current_partial"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setup(t)
			seedEmbyIdentityRestoreDatabase(t, conn)
			files := map[string]string{
				"Migrator.json":          "{\"id\":1,\"version_code\":70}\n",
				"EmbyMediaSyncFile.json": "{\"id\":1,\"emby_item_id\":42,\"sync_file_id\":12}\n",
				"EmbyMediaItem.json":     "{\"id\":1,\"item_id\":\"source-item\",\"item_id_int\":42}\n",
			}
			switch scenario {
			case "conflict":
				files["EmbyMediaItem.json"] += "{\"id\":2,\"item_id\":\"different-item\",\"item_id_int\":42}\n"
			case "missing_media":
				delete(files, "EmbyMediaItem.json")
			case "current_partial":
				delete(files, "EmbyMediaItem.json")
				files["Migrator.json"] = fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", models.MaxVersionCode)
				files["EmbyMediaSyncFile.json"] = fmt.Sprintf("{\"id\":1,\"emby_item_id\":%q,\"sync_file_id\":12}\n", restoredGUID)
			case "null_link":
				files["EmbyMediaSyncFile.json"] = "{\"id\":1,\"emby_item_id\":null,\"sync_file_id\":12}\n"
			default:
				value := map[string]string{"negative": "-1", "fraction": "42.5", "overflow": "9223372036854775808", "text": "\"42\""}[scenario]
				files["EmbyMediaSyncFile.json"] = fmt.Sprintf("{\"id\":1,\"emby_item_id\":%s,\"sync_file_id\":12}\n", value)
			}
			archive := writeBackupArchive(t, files, zip.Deflate)
			err := Restore(archive)
			if scenario == "null_link" || scenario == "current_partial" {
				if err != nil {
					t.Fatal(err)
				}
				var stored *string
				if err := conn.Table("emby_media_sync_files").Select("emby_item_id").Where("id = ?", 1).Row().Scan(&stored); err != nil {
					t.Fatal(err)
				}
				if scenario == "null_link" && stored != nil {
					t.Fatal("partial restore replaced the NULL identity")
				}
				if scenario == "current_partial" {
					var media models.EmbyMediaItem
					if err := conn.First(&media, 1).Error; err != nil || media.ItemId != "00042" || stored == nil || *stored != restoredGUID {
						t.Fatalf("current partial restore changed unrelated media or its string identity: %+v %v", media, err)
					}
				}
				return
			}
			if !errors.Is(err, ErrArchiveInvalid) || GetRunningResult().RestoreOutcome != "not_started" {
				t.Fatalf("invalid identity reached restore transaction: %v, %+v", err, GetRunningResult())
			}
			assertRestoredEmbyIdentities(t, conn)
			if scenario == "missing_media" && !strings.Contains(err.Error(), "同一备份") {
				t.Fatalf("missing dependency did not explain the required source table: %v", err)
			}
		})
	}
}
