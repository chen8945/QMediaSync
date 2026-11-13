package controllers

import (
	"path/filepath"
	"sync"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/directoryupload"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var controllerTestDBMu sync.Mutex

func setupControllerTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()
	return setupControllerTestDBAt(t, ":memory:", models...)
}

func setupControllerTestDBAt(t *testing.T, filename string, models ...any) *gorm.DB {
	t.Helper()

	testDB, err := gorm.Open(sqlite.Open(filename), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开测试数据库失败: %v", err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatalf("读取底层测试数据库失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)

	controllerTestDBMu.Lock()
	originalDB := db.Db
	originalRunner := runAuthBackgroundTask
	db.Db = testDB
	runAuthBackgroundTask = func(fn func()) {
		fn()
	}
	t.Cleanup(func() {
		directoryupload.StopDirectoryUploadService()
		if filename != ":memory:" {
			_ = sqlDB.Close()
		}
		db.Db = originalDB
		runAuthBackgroundTask = originalRunner
		controllerTestDBMu.Unlock()
	})

	if len(models) > 0 {
		if err := db.Db.AutoMigrate(models...); err != nil {
			t.Fatalf("迁移测试表失败: %v", err)
		}
	}
	return testDB
}

func setupConcurrentControllerTestDB(t *testing.T, models ...any) *gorm.DB {
	t.Helper()

	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "controller.db")+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"), &gorm.Config{
		SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatalf("打开并发测试数据库失败: %v", err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatalf("读取底层并发测试数据库失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	sqlDB.SetMaxIdleConns(8)

	controllerTestDBMu.Lock()
	originalDB := db.Db
	originalRunner := runAuthBackgroundTask
	db.Db = testDB
	runAuthBackgroundTask = func(fn func()) {
		fn()
	}
	t.Cleanup(func() {
		directoryupload.StopDirectoryUploadService()
		_ = sqlDB.Close()
		db.Db = originalDB
		runAuthBackgroundTask = originalRunner
		controllerTestDBMu.Unlock()
	})

	if len(models) > 0 {
		if err := db.Db.AutoMigrate(models...); err != nil {
			t.Fatalf("迁移并发测试表失败: %v", err)
		}
	}
	return testDB
}
