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

func TestRestoreFnosProxyCompatibility(t *testing.T) {
	testRestoreFnosProxyCompatibility(t, setupBackupTest)
}

func TestFnosProxyBackupExtraColumns(t *testing.T) {
	testFnosProxyBackupExtraColumns(t, setupBackupTest)
}

func testFnosProxyBackupExtraColumns(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	conn := setup(t)
	seedSchemaCompatibilityDatabase(t, conn)
	models.AllTables = []any{&models.FnosProxyConfig{}, &models.Migrator{}}
	if err := conn.AutoMigrate(&models.FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		"ALTER TABLE fnos_proxy_config ADD COLUMN listen_port INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE fnos_proxy_config ADD COLUMN https_enabled BOOLEAN NOT NULL DEFAULT FALSE",
		"ALTER TABLE fnos_proxy_config ADD COLUMN https_port INTEGER NOT NULL DEFAULT 0",
	} {
		if err := conn.Exec(query).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := models.SaveFnosProxyConfig(context.Background(), models.FnosProxyConfig{
		Enabled: true, FnosURL: "http://fnos.invalid:8005", PathMappings: []models.FnosPathMapping{{FnosRoot: "/fnos", LocalRoot: t.TempDir()}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("UPDATE fnos_proxy_config SET listen_port = 9005, https_enabled = TRUE, https_port = 9006 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatalf("old test columns must not prevent new backups: %v", err)
	}
	manifest, err := prepareLogicalBackup(dir, conn)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"listen_port", "https_enabled", "https_port"} {
		if !slices.ContainsFunc(manifest.Excluded, func(item logicalExclusion) bool {
			return item.Name == "fnos_proxy_config" && item.Column == name
		}) {
			t.Fatalf("missing exclusion for obsolete column %s", name)
		}
		for _, table := range manifest.Tables {
			if table.Name == "fnos_proxy_config" && slices.ContainsFunc(table.Columns, func(column logicalColumn) bool { return column.Name == name }) {
				t.Fatalf("obsolete column exported as business data: %s", name)
			}
		}
	}
	archive := filepath.Join(t.TempDir(), "extra-columns.zip")
	if err := helpers.ZipDir(dir, archive); err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive); err != nil {
		t.Fatal(err)
	}
	if conn.Dialector.Name() == "postgres" {
		// 恢复后正式进程要求重启；新连接避免继续使用旧表结构的查询计划。
		pool, err := conn.DB()
		if err != nil {
			t.Fatal(err)
		}
		pool.SetMaxIdleConns(0)
		pool.SetMaxIdleConns(1)
	}
	config, err := models.ReadFnosProxyConfig(context.Background())
	if err != nil || !config.Enabled || config.FnosURL != "http://fnos.invalid:8005" || len(config.PathMappings) != 1 {
		t.Fatalf("backup lost active configuration: %+v %v", config, err)
	}
	if err := conn.Exec("ALTER TABLE fnos_proxy_config ADD COLUMN unknown_test_column INTEGER").Error; err != nil {
		t.Fatal(err)
	}
	if err := writeLogicalBackup(conn, t.TempDir(), nil); err == nil {
		t.Fatal("unknown columns must still be rejected")
	}
}

func TestFnosProxyHistoricalExclusionsStayStrict(t *testing.T) {
	for _, version := range []int{67, 68} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			conn := setupBackupTest(t)
			seedSchemaCompatibilityDatabase(t, conn)
			models.AllTables = append(models.AllTables, &models.FnosProxyConfig{})
			if err := conn.AutoMigrate(&models.FnosProxyConfig{}); err != nil {
				t.Fatal(err)
			}
			dir := fnosCompatibilityBackupDirectory(t, conn, version)
			manifest, err := prepareLogicalBackup(dir, conn)
			if err != nil {
				t.Fatal(err)
			}
			manifest.Excluded = append(manifest.Excluded, logicalExclusion{
				Name: "fnos_proxy_config", Column: "listen_port", Reason: "retired column is no longer used by the application",
			})
			writeCompatibilityManifest(t, dir, manifest)
			if _, err := prepareLogicalBackup(dir, conn); !errors.Is(err, ErrArchiveInvalid) {
				t.Fatalf("historical schema must not accept exclusions from a future table: %v", err)
			}
		})
	}
}

func testRestoreFnosProxyCompatibility(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, format := range []string{"logical69", "logical69_disabled", "logical68", "logical67", "legacy68", "legacy67", "legacy66"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/rollback_%t", format, fail), func(t *testing.T) {
				conn := setup(t)
				seedSchemaCompatibilityDatabase(t, conn)
				models.AllTables = append(models.AllTables, &models.FnosProxyConfig{})
				if err := conn.AutoMigrate(&models.FnosProxyConfig{}); err != nil {
					t.Fatal(err)
				}
				source := models.FnosProxyConfig{
					Enabled: true, FnosURL: "http://source.invalid:8005",
					PathMappings: []models.FnosPathMapping{{FnosRoot: "/fnos", LocalRoot: t.TempDir()}},
				}
				if format == "logical69_disabled" {
					source = models.FnosProxyConfig{PathMappings: []models.FnosPathMapping{}}
				}
				if err := models.SaveFnosProxyConfig(context.Background(), source); err != nil {
					t.Fatal(err)
				}
				var archive string
				if strings.HasPrefix(format, "logical") {
					version := 69
					if format == "logical68" {
						version = 68
					} else if format == "logical67" {
						version = 67
					}
					dir := fnosCompatibilityBackupDirectory(t, conn, version)
					archive = filepath.Join(t.TempDir(), "fnos.zip")
					if err := helpers.ZipDir(dir, archive); err != nil {
						t.Fatal(err)
					}
				} else {
					archive = writeBackupArchive(t, map[string]string{
						"Migrator.json": fmt.Sprintf("{\"id\":1,\"version_code\":%s}\n", strings.TrimPrefix(format, "legacy")),
					}, zip.Deflate)
				}
				target := source
				target.FnosURL = "http://target.invalid:8005"
				target.Enabled = true
				if err := models.SaveFnosProxyConfig(context.Background(), target); err != nil {
					t.Fatal(err)
				}
				injected := errors.New("injected restore table creation failure")
				if fail {
					if err := conn.Callback().Raw().Before("gorm:raw").Register("test:fnos_restore_index", func(tx *gorm.DB) {
						if strings.Contains(tx.Statement.SQL.String(), "CREATE TABLE") && strings.Contains(tx.Statement.SQL.String(), "fnos_proxy_config") {
							tx.AddError(injected)
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				err := Restore(archive)
				if fail {
					if !errors.Is(err, injected) || GetRunningResult().RestoreOutcome != "rolled_back" {
						t.Fatalf("restore must roll back: %v %+v", err, GetRunningResult())
					}
				} else if err != nil {
					t.Fatal(err)
				}
				got, readErr := models.ReadFnosProxyConfig(context.Background())
				if readErr != nil {
					t.Fatal(readErr)
				}
				if fail {
					if got.FnosURL != target.FnosURL || !got.Enabled {
						t.Fatalf("failed restore lost target config: %+v", got)
					}
				} else if strings.HasPrefix(format, "logical69") {
					if got.FnosURL != source.FnosURL || got.Enabled != source.Enabled || !reflect.DeepEqual(got.PathMappings, source.PathMappings) {
						t.Fatalf("current backup lost configuration or hidden JSON: %+v", got)
					}
				} else if got.ID != 0 || got.Enabled || len(got.PathMappings) != 0 {
					t.Fatalf("old backup retained target FNOS config: %+v", got)
				}
				var version models.Migrator
				if err := conn.First(&version).Error; err != nil || version.VersionCode != models.MaxVersionCode {
					t.Fatalf("version mismatch: %+v %v", version, err)
				}
			})
		}
	}
}

// fnosCompatibilityBackupDirectory 独立删掉历史版本不存在的表，保留旧包的严格清单。
func fnosCompatibilityBackupDirectory(t *testing.T, conn *gorm.DB, version int) string {
	t.Helper()
	if version == 67 {
		dir, _ := schema67BackupDirectory(t, conn)
		return dir
	}
	dir := t.TempDir()
	if err := writeLogicalBackup(conn, dir, nil); err != nil {
		t.Fatal(err)
	}
	if version == models.MaxVersionCode {
		return dir
	}
	manifest, err := prepareLogicalBackup(dir, conn)
	if err != nil {
		t.Fatal(err)
	}
	manifest.SchemaVersion = version
	if version < 69 {
		manifest.Excluded = slices.DeleteFunc(manifest.Excluded, func(item logicalExclusion) bool {
			return item.Name == "fnos_proxy_config"
		})
	}
	var tables []logicalTable
	for _, table := range manifest.Tables {
		if version < 69 && table.Name == "fnos_proxy_config" {
			if err := os.Remove(filepath.Join(dir, table.File)); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if table.Name == "migrator" {
			var buffer bytes.Buffer
			if err := readLogicalRows(dir, table, func(row map[string]any) error {
				row["version_code"] = int64(version)
				return json.NewEncoder(&buffer).Encode(row)
			}); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, table.File), buffer.Bytes(), 0600); err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(buffer.Bytes())
			table.SHA256 = hex.EncodeToString(sum[:])
		}
		tables = append(tables, table)
	}
	manifest.Tables = tables
	writeCompatibilityManifest(t, dir, manifest)
	return dir
}
