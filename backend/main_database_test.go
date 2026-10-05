package main

import (
	"io"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestStartDatabaseRejectsInitializationFailure(t *testing.T) {
	previousDB, previousConfig, previousDir, previousLogger := db.Db, helpers.GlobalConfig, helpers.ConfigDir, helpers.AppLogger
	helpers.ConfigDir = t.TempDir()
	helpers.GlobalConfig.Db = helpers.ConfigDb{Engine: helpers.DbEngineSqlite, SqliteFile: "startup.db"}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		if db.Db != nil && db.Db != previousDB {
			conn, err := db.Db.DB()
			if err != nil {
				t.Error(err)
			} else if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
		db.Db, helpers.GlobalConfig, helpers.ConfigDir, helpers.AppLogger = previousDB, previousConfig, previousDir, previousLogger
	})
	filename := filepath.Join(helpers.ConfigDir, "startup.db")
	fixture, err := gorm.Open(sqlite.Open(filename), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	// 同名视图使后段建表真实失败；初始化不得覆盖该现存对象或留下成功版本。
	if err := fixture.Exec("CREATE VIEW emby_webhook_targets AS SELECT 1 AS id").Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := fixture.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if err := app.StartDatabase(); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("database startup did not return initialization failure: %v", err)
	}
	if db.Db.Migrator().HasTable(&models.Migrator{}) || db.Db.Migrator().HasTable(&models.EmbyConfig{}) {
		t.Fatal("failed database startup retained initialization tables")
	}
	var marker int
	if err := db.Db.Raw("SELECT id FROM emby_webhook_targets").Scan(&marker).Error; err != nil || marker != 1 {
		t.Fatalf("failed initialization changed existing object: marker=%d err=%v", marker, err)
	}
	if err := db.Db.Exec("DROP VIEW emby_webhook_targets").Error; err != nil {
		t.Fatal(err)
	}
	failedConnection, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := failedConnection.Close(); err != nil {
		t.Fatal(err)
	}
	if err := app.StartDatabase(); err != nil {
		t.Fatal(err)
	}
	var version models.Migrator
	if err := db.Db.First(&version).Error; err != nil || version.VersionCode != models.MaxVersionCode {
		t.Fatalf("retry did not initialize complete version: %+v err=%v", version, err)
	}
}
