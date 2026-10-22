package models

import (
	"context"
	"reflect"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestFnosProxyConfigSQLite(t *testing.T) {
	setupFreshInitializationSQLite(t)
	testFnosProxyConfigPersistence(t, db.Db)
}

func TestFnosProxyConfigExtraColumnsSQLite(t *testing.T) {
	setupFreshInitializationSQLite(t)
	testFnosProxyConfigExtraColumns(t, db.Db)
}

func testFnosProxyConfigExtraColumns(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := conn.AutoMigrate(&FnosProxyConfig{}); err != nil {
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
	ctx := context.Background()
	if err := SaveFnosProxyConfig(ctx, FnosProxyConfig{Enabled: true, FnosURL: "http://fnos.invalid:8005"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("UPDATE fnos_proxy_config SET listen_port = 9005, https_enabled = TRUE, https_port = 9006 WHERE id = 1").Error; err != nil {
		t.Fatal(err)
	}
	config, err := ReadFnosProxyConfig(ctx)
	if err != nil || !config.Enabled || config.FnosURL != "http://fnos.invalid:8005" {
		t.Fatalf("old test table must remain readable: %+v %v", config, err)
	}
	if err := conn.AutoMigrate(&FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := SaveFnosProxyConfig(ctx, FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	config, err = ReadFnosProxyConfig(ctx)
	if err != nil || config.Enabled || config.FnosURL != "" || len(config.PathMappings) != 0 {
		t.Fatalf("old test table must accept current configuration: %+v %v", config, err)
	}
	var obsolete struct {
		ListenPort   int
		HTTPSEnabled bool `gorm:"column:https_enabled"`
		HTTPSPort    int  `gorm:"column:https_port"`
	}
	if err := conn.Table("fnos_proxy_config").First(&obsolete, "id = ?", 1).Error; err != nil ||
		obsolete.ListenPort != 9005 || !obsolete.HTTPSEnabled || obsolete.HTTPSPort != 9006 {
		t.Fatalf("saving must not modify or drop unused columns: %+v %v", obsolete, err)
	}
}

func testFnosProxyConfigPersistence(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := conn.AutoMigrate(&FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	config, err := ReadFnosProxyConfig(ctx)
	if err != nil || config.Enabled || config.PathMappings == nil {
		t.Fatalf("default configuration: %+v %v", config, err)
	}
	var count int64
	if err := conn.Model(&FnosProxyConfig{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("reading must not insert: %d %v", count, err)
	}
	config = FnosProxyConfig{
		Enabled: true, FnosURL: "http://fnos.invalid:8005",
		PathMappings: []FnosPathMapping{{FnosRoot: `c:\Media\`, LocalRoot: t.TempDir()}},
	}
	if err := SaveFnosProxyConfig(ctx, config); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFnosProxyConfig(ctx)
	if err != nil || got.ID != 1 || !got.Enabled || got.PathMappings[0].FnosRoot != "C:/Media" || got.CreatedAt == 0 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	if err := SaveFnosProxyConfig(ctx, FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	got, err = ReadFnosProxyConfig(ctx)
	if err != nil || got.Enabled || got.FnosURL != "" || len(got.PathMappings) != 0 || got.PathMappingsJSON != "[]" {
		t.Fatalf("zero values must overwrite old settings: %+v %v", got, err)
	}
	for _, malformed := range []string{"{", "null"} {
		if err := conn.Model(&FnosProxyConfig{}).Where("id = 1").UpdateColumn("path_mappings_json", malformed).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := ReadFnosProxyConfig(ctx); err == nil {
			t.Fatal("corrupt persisted mapping must fail reads")
		}
		if err := SaveFnosProxyConfig(ctx, config); err == nil {
			t.Fatal("saving must not silently overwrite corrupt configuration")
		}
		var stored string
		if err := conn.Model(&FnosProxyConfig{}).Select("path_mappings_json").Scan(&stored).Error; err != nil || stored != malformed {
			t.Fatalf("failed save overwrote the original data: %q %v", stored, err)
		}
	}
	if err := conn.Migrator().DropTable(&FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFnosProxyConfig(ctx); err == nil {
		t.Fatal("query errors must not turn into defaults")
	}
	if err := SaveFnosProxyConfig(ctx, config); err == nil {
		t.Fatal("missing table must not report successful saves")
	}
}

func TestFnosProxyMigrationSQLite(t *testing.T) {
	for _, scenario := range []string{"upgrade", "table_failure", "version_failure"} {
		t.Run(scenario, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testFnosProxyMigration(t, db.Db, reopen, scenario)
		})
	}
}

func testFnosProxyMigration(t *testing.T, conn *gorm.DB, reopen func() *gorm.DB, scenario string) {
	t.Helper()
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	var original Settings
	if err := conn.First(&original).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Migrator().DropTable(&FnosProxyConfig{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&Migrator{}).Where("id = 1").UpdateColumn("version_code", 68).Error; err != nil {
		t.Fatal(err)
	}
	if scenario != "upgrade" {
		failure := freshInitializationFailure{name: scenario, operation: "raw", sql: "CREATE TABLE", model: &FnosProxyConfig{}}
		if scenario == "version_failure" {
			failure.operation, failure.model = "update", &Migrator{}
		}
		remove, injected, _ := failFreshInitialization(t, conn, failure)
		err := Migrate()
		if err == nil || !*injected {
			t.Fatalf("migration failure must propagate: %v", err)
		}
		remove()
		if conn.Migrator().HasTable(&FnosProxyConfig{}) {
			t.Fatal("failed migration retained new table")
		}
		assertEmbyMigrationVersion(t, conn, 68)
		conn = reopen()
		assertEmbyMigrationVersion(t, conn, 68)
		if conn.Migrator().HasTable(&FnosProxyConfig{}) {
			t.Fatal("reopened failed migration retained new table")
		}
	}
	for range 2 {
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		assertEmbyMigrationVersion(t, conn, MaxVersionCode)
		var count int64
		if err := conn.Model(&FnosProxyConfig{}).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("migration must leave defaults absent: %d %v", count, err)
		}
		var got Settings
		if err := conn.First(&got).Error; err != nil || !reflect.DeepEqual(got, original) {
			t.Fatalf("migration changed old settings: %+v %v", got, err)
		}
		conn = reopen()
	}
}
