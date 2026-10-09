package backup

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestoreEmbySyncSchemaCompatibility(t *testing.T) {
	testRestoreEmbySyncSchemaCompatibility(t, setupBackupTest)
}

func testRestoreEmbySyncSchemaCompatibility(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"logical67", "legacy67", "legacy66"} {
		for _, failIndex := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/index_failure_%t", format, failIndex), func(t *testing.T) {
				conn := setup(t)
				config, root := seedSchemaCompatibilityDatabase(t, conn)
				var archive string
				if format == "logical67" {
					dir, _ := schema67BackupDirectory(t, conn)
					archive = filepath.Join(t.TempDir(), "schema67.zip")
					if err := helpers.ZipDir(dir, archive); err != nil {
						t.Fatal(err)
					}
				} else {
					version := 67
					if format == "legacy66" {
						version = 66
					}
					archive = writeBackupArchive(t, map[string]string{
						"Migrator.json":   fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", version),
						"EmbyConfig.json": "{\"id\":1,\"emby_url\":\"http://source.invalid\",\"last_sync_time\":0}\n",
						"SyncFile.json":   "{\"id\":12,\"sync_path_id\":7,\"file_id\":\"video\",\"path\":\"/media\"}\n",
					}, zip.Deflate)
				}
				if err := conn.Model(&models.EmbyConfig{}).Where("id = ?", config.ID).UpdateColumn("emby_url", "http://target.invalid").Error; err != nil {
					t.Fatal(err)
				}
				indexErr := errors.New("schema compatibility index failure")
				if failIndex {
					if err := conn.Callback().Raw().Before("gorm:raw").Register("test:schema_compatibility_index", func(tx *gorm.DB) {
						if strings.Contains(tx.Statement.SQL.String(), "CREATE INDEX IF NOT EXISTS idx_sync_files_sibling_path") {
							tx.AddError(indexErr)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := Restore(archive)
				var restored models.EmbyConfig
				if readErr := conn.First(&restored, config.ID).Error; readErr != nil {
					t.Fatal(readErr)
				}
				var roots []models.SyncRootEvidence
				if readErr := conn.Find(&roots).Error; readErr != nil {
					t.Fatal(readErr)
				}
				var version models.Migrator
				if readErr := conn.First(&version).Error; readErr != nil || version.VersionCode != models.MaxVersionCode {
					t.Fatalf("restored schema version: %+v, %v", version, readErr)
				}
				if failIndex {
					if !errors.Is(err, indexErr) || GetRunningResult().RestoreOutcome != "rolled_back" {
						t.Fatalf("expected confirmed rollback: %v, %+v", err, GetRunningResult())
					}
					if restored.EmbyUrl != "http://target.invalid" || restored.PathMappingsJSON != config.PathMappingsJSON || !reflect.DeepEqual(roots, []models.SyncRootEvidence{root}) {
						t.Fatal("failed adaptation changed target configuration or root authority")
					}
				} else {
					if err != nil {
						t.Fatal(err)
					}
					if restored.EmbyUrl != config.EmbyUrl || restored.PathMappingsJSON != "[]" || restored.LastSyncTime != 0 || len(roots) != 0 {
						t.Fatalf("old backup did not restore explicit empty defaults: %+v, roots=%d", restored, len(roots))
					}
					root.ID = 0
					if err := conn.Create(&root).Error; err != nil {
						t.Fatal(err)
					}
					root.ID = 0
					if err := conn.Create(&root).Error; err == nil {
						t.Fatal("restored root evidence table lost the unique sync-path index")
					}
				}
				if !conn.Migrator().HasIndex(&models.SyncFile{}, "idx_sync_files_sibling_path") ||
					!conn.Migrator().HasIndex(&models.SyncRootEvidence{}, "idx_sync_root_evidences_sync_path_id") {
					t.Fatal("required indexes were not rebuilt or preserved by rollback")
				}
			})
		}
	}
}

func seedSchemaCompatibilityDatabase(t *testing.T, conn *gorm.DB) (models.EmbyConfig, models.SyncRootEvidence) {
	t.Helper()
	// 故意缩减模型清单，版本适配不能凭空增加未注册的业务模型。
	models.AllTables = []any{&models.EmbyConfig{}, &models.SyncRootEvidence{}, &models.Migrator{}, &models.SyncFile{}}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	if err := models.EnsureSyncFileLookupIndexes(conn); err != nil {
		t.Fatal(err)
	}
	if err := conn.Where("1 = 1").Delete(&models.Migrator{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&models.Migrator{BaseModel: models.BaseModel{ID: 1}, VersionCode: models.MaxVersionCode}).Error; err != nil {
		t.Fatal(err)
	}
	config := models.EmbyConfig{BaseModel: models.BaseModel{ID: 1}, EmbyUrl: "http://source.invalid", PathMappingsJSON: `[{"emby_root":"/emby","local_root":"/strm","sync_path_id":7}]`}
	root := models.SyncRootEvidence{
		BaseModel: models.BaseModel{ID: 11}, SyncPathID: 7, SourceType: models.SourceType115,
		AccountID: 3, AccountIdentity: "account-3", RootFileID: "42", RootPath: "/media",
		AncestorsJSON: `[{"file_id":"0","path":"/"},{"file_id":"42","parent_id":"0","path":"/media"}]`,
		ObservedAt:    100, InvalidatedAt: 110,
	}
	for _, row := range []any{&config, &root, &models.SyncFile{BaseModel: models.BaseModel{ID: 12}, SyncPathId: 7, FileId: "video", Path: "/media"}} {
		if err := conn.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	return config, root
}

// schema67BackupDirectory 从真实导出中只移除版本 67 没有的字段和表，独立于生产适配器。
func schema67BackupDirectory(t *testing.T, conn *gorm.DB) (string, *logicalManifest) {
	t.Helper()
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatal(err)
	}
	manifest, err := prepareLogicalBackup(dir, conn)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = 67
	var tables []logicalTable
	for _, table := range manifest.Tables {
		if table.Name == "sync_root_evidences" {
			if err := os.Remove(filepath.Join(dir, table.File)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if table.Name == "emby_config" || table.Name == "migrator" {
			var content bytes.Buffer
			if err := readLogicalRows(dir, table, func(row map[string]any) error {
				if table.Name == "emby_config" {
					delete(row, "path_mappings_json")
				} else {
					row["version_code"] = int64(67)
				}
				return json.NewEncoder(&content).Encode(row)
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, table.File), content.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(content.Bytes())
			table.SHA256 = hex.EncodeToString(sum[:])
			if table.Name == "emby_config" {
				table.Columns = slices.DeleteFunc(table.Columns, func(column logicalColumn) bool { return column.Name == "path_mappings_json" })
			}
		}
		tables = append(tables, table)
	}
	manifest.Tables = tables
	writeCompatibilityManifest(t, dir, manifest)
	return dir, manifest
}

func writeCompatibilityManifest(t *testing.T, dir string, manifest *logicalManifest) {
	t.Helper()
	content, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, logicalManifestFile), content, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareSchema67BackupRetainsStrictValidation(t *testing.T) {
	for _, scenario := range []string{"valid", "schema66", "schema68", "new_column", "missing_column", "extra_file", "missing_table", "checksum", "wrong_row_version"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			seedSchemaCompatibilityDatabase(t, conn)
			dir, manifest := schema67BackupDirectory(t, conn)
			config := &manifest.Tables[0]
			switch scenario {
			case "schema66":
				manifest.SchemaVersion = 66
			case "schema68":
				manifest.SchemaVersion = 68
			case "new_column":
				config.Columns = append(config.Columns, logicalColumn{Name: "path_mappings_json", Type: "text"})
			case "missing_column":
				config.Columns = config.Columns[1:]
			case "extra_file":
				if err := os.WriteFile(filepath.Join(dir, "tables", "SyncRootEvidence.json"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "missing_table":
				manifest.Tables = manifest.Tables[:len(manifest.Tables)-1]
			case "checksum":
				config.SHA256 = strings.Repeat("0", 64)
			case "wrong_row_version":
				for i := range manifest.Tables {
					table := &manifest.Tables[i]
					if table.Name != "migrator" {
						continue
					}
					content := []byte("{\"id\":1,\"created_at\":0,\"updated_at\":0,\"version_code\":68}\n")
					if err := os.WriteFile(filepath.Join(dir, table.File), content, 0600); err != nil {
						t.Fatal(err)
					}
					sum := sha256.Sum256(content)
					table.SHA256 = hex.EncodeToString(sum[:])
				}
			}
			writeCompatibilityManifest(t, dir, manifest)
			_, err := prepareRestorePlan(dir, conn)
			if scenario == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else if !errors.Is(err, ErrArchiveInvalid) && !errors.Is(err, ErrArchiveUnsupported) {
				t.Fatalf("invalid adapted archive accepted: %v", err)
			}
		})
	}
}

func TestRestoreLegacyRootEvidenceDependencies(t *testing.T) {
	testRestoreLegacyRootEvidenceDependencies(t, setupBackupTest)
}

func testRestoreLegacyRootEvidenceDependencies(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, replacement := range []struct {
		name string
		file string
		row  string
	}{
		{"account", "Account.json", `{"id":3,"source_type":"115","user_id":"restored-user"}`},
		{"sync_paths", "SyncPath.json", `{"id":7,"account_id":3,"source_type":"115","base_cid":"replacement-root"}`},
		{"sync_files", "SyncFile.json", `{"id":12,"sync_path_id":7,"file_id":"replacement-video"}`},
		{"unrelated", "backupTestItem.json", `{"ID":1,"Name":"restored"}`},
	} {
		for _, stage := range []string{"preflight", "after_maintenance"} {
			t.Run(replacement.name+"/"+stage, func(t *testing.T) {
				conn := setup(t)
				config, root := seedSchemaCompatibilityDatabase(t, conn)
				models.AllTables = append(models.AllTables, &models.Account{}, &models.SyncPath{}, &backupTestItem{})
				if err := conn.AutoMigrate(&models.Account{}, &models.SyncPath{}, &backupTestItem{}); err != nil {
					t.Fatal(err)
				}
				account := models.Account{BaseModel: models.BaseModel{ID: 3}, SourceType: models.SourceType115, UserId: "original-user"}
				path := models.SyncPath{BaseModel: models.BaseModel{ID: 7}, SourceType: account.SourceType, AccountId: account.ID, BaseCid: root.RootFileID, RemotePath: root.RootPath}
				for _, value := range []any{&account, &path, &backupTestItem{ID: 1, Name: "original"}} {
					if err := conn.Create(value).Error; err != nil {
						t.Fatal(err)
					}
				}
				if stage == "after_maintenance" {
					if err := conn.Migrator().DropTable(&models.SyncRootEvidence{}); err != nil {
						t.Fatal(err)
					}
				}
				entered := false
				beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
					entered = true
					if stage == "after_maintenance" {
						if err := conn.AutoMigrate(&models.SyncRootEvidence{}); err != nil {
							return nil, err
						}
						if err := conn.Create(&root).Error; err != nil {
							return nil, err
						}
					}
					return &restoreMaintenance{database: conn}, nil
				}
				archive := writeBackupArchive(t, map[string]string{
					"Migrator.json":  fmt.Sprintf("{\"id\":1,\"version_code\":%d}\n", models.MaxVersionCode),
					replacement.file: replacement.row + "\n",
				}, zip.Store)
				err := Restore(archive)
				if replacement.name == "unrelated" {
					var restored backupTestItem
					if err != nil || !entered {
						t.Fatalf("unrelated partial restore was rejected: %v", err)
					}
					if err := conn.First(&restored, 1).Error; err != nil || restored.Name != "restored" {
						t.Fatalf("unrelated row was not restored: %+v %v", restored, err)
					}
				} else {
					if !errors.Is(err, ErrArchiveUnsupported) || !strings.Contains(err.Error(), "sync_root_evidences") {
						t.Fatalf("missing root authority dependency was accepted: %v", err)
					}
					wantOutcome := "not_started"
					if stage == "after_maintenance" {
						wantOutcome = "rolled_back"
					}
					if entered != (stage == "after_maintenance") || GetRunningResult().RestoreOutcome != wantOutcome {
						t.Fatalf("dependency check ran at the wrong boundary: entered=%t result=%+v", entered, GetRunningResult())
					}
					var restoredAccount models.Account
					var restoredPath models.SyncPath
					var restoredFile models.SyncFile
					if err := conn.First(&restoredAccount, account.ID).Error; err != nil || !reflect.DeepEqual(restoredAccount, account) {
						t.Fatalf("rejected restore changed the account: %+v %v", restoredAccount, err)
					}
					if err := conn.First(&restoredPath, path.ID).Error; err != nil || !reflect.DeepEqual(restoredPath, path) {
						t.Fatalf("rejected restore changed the sync path: %+v %v", restoredPath, err)
					}
					if err := conn.First(&restoredFile, 12).Error; err != nil || restoredFile.FileId != "video" {
						t.Fatalf("rejected restore changed the ledger: %+v %v", restoredFile, err)
					}
				}
				var restoredRoot models.SyncRootEvidence
				var restoredConfig models.EmbyConfig
				if err := conn.First(&restoredRoot, root.ID).Error; err != nil || !reflect.DeepEqual(restoredRoot, root) {
					t.Fatalf("partial restore changed root evidence: %+v %v", restoredRoot, err)
				}
				if err := conn.First(&restoredConfig, config.ID).Error; err != nil || restoredConfig.PathMappingsJSON != config.PathMappingsJSON {
					t.Fatalf("partial restore changed path mappings: %+v %v", restoredConfig, err)
				}
			})
		}
	}
}
