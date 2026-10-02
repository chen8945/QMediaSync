package models

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/syncscope"
)

func TestSyncPositionCacheColdHotAndContentUpdate(t *testing.T) {
	sp := setupSyncScopeTest(t)
	testSyncPositionCacheColdHot(t, sp)
}

func testSyncPositionCacheColdHot(t *testing.T, sp *SyncPath) {
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: sp.RemotePath, LocalFilePath: filepath.Join(sp.GetFullLocalPath(), "movie.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	queries := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("count_positions", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			if _, ok := tx.Statement.Dest.(*[]SyncFile); ok {
				queries++
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("count_positions")
	acquire := func() {
		t.Helper()
		_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
		if err != nil {
			t.Fatal(err)
		}
		release()
	}
	acquire()
	if queries != 1 {
		t.Fatalf("cold queries=%d", queries)
	}
	acquire()
	if queries != 1 {
		t.Fatalf("hot queries=%d", queries)
	}
	file.MTime++
	file.FileSize++
	file.Sha1 = "changed"
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 1 {
		t.Fatalf("content-only update discarded cache: %d", queries)
	}
	file.LocalFilePath = filepath.Join(t.TempDir(), "moved.strm")
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 2 {
		t.Fatalf("move must rebuild: %d", queries)
	}
	finish := BeginSyncPositionMutation()
	err := db.Db.Delete(file).Error
	finish()
	if err != nil {
		t.Fatal(err)
	}
	acquire()
	if queries != 3 {
		t.Fatalf("delete must rebuild: %d", queries)
	}
}

func TestSyncPositionCacheRevalidatesSymlinkAcrossRounds(t *testing.T) {
	sp := setupSyncScopeTest(t)
	root := sp.GetFullLocalPath()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked")
	if err := os.Mkdir(link, 0755); err != nil {
		t.Fatal(err)
	}
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: sp.RemotePath, LocalFilePath: filepath.Join(link, "file.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	busy, err := syncscope.Acquire(t.Context(), syncscope.Scope{LocalPath: outside})
	if err != nil {
		t.Fatal(err)
	}
	defer busy()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	_, release, err = AcquireSyncPathScope(ctx, sp.ID)
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("cached logical path skipped moved symlink")
	}
}

func TestSyncPositionCacheRejectsBuildDuringMutation(t *testing.T) {
	sp := setupSyncScopeTest(t)
	count := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("mutate_during_positions", func(tx *gorm.DB) {
		if tx.Statement.Table != "sync_files" {
			return
		}
		count++
		if count == 1 {
			finish := BeginSyncPositionMutation()
			finish()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("mutate_during_positions")
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if count != 2 {
		t.Fatalf("must discard interrupted build, queries=%d", count)
	}
}

func TestSyncPositionCachePostgres(t *testing.T) {
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN")
	}
	conn, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	schema := fmt.Sprintf("qms_scope_%d", time.Now().UnixNano())
	if err := conn.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	previous := db.Db
	db.Db = conn
	t.Cleanup(func() { db.Db = previous; conn.Exec("DROP SCHEMA " + schema + " CASCADE"); sqlDB.Close() })
	if err := conn.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.AutoMigrate(&SyncPath{}, &SyncFile{}); err != nil {
		t.Fatal(err)
	}
	sp := &SyncPath{SourceType: SourceType115, AccountId: 1, BaseCid: "root", LocalPath: t.TempDir(), RemotePath: "/movies"}
	if err := conn.Create(sp).Error; err != nil {
		t.Fatal(err)
	}
	testSyncPositionCacheColdHot(t, sp)
}

func TestSyncPositionCacheResetPartialFailure(t *testing.T) {
	sp := setupSyncScopeTest(t)
	file := &SyncFile{SyncPathId: sp.ID, SourceType: sp.SourceType, AccountId: sp.AccountId, Path: "/old", LocalFilePath: filepath.Join(t.TempDir(), "old.strm")}
	if err := SaveSyncFilePosition(db.Db, file); err != nil {
		t.Fatal(err)
	}
	before, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", "")
	if err != nil || len(before) != 1 {
		t.Fatalf("warm: %v %v", before, err)
	}
	tables := AllTables
	AllTables = []any{&SyncFile{}, &SyncPath{}}
	t.Cleanup(func() { AllTables = tables })
	if err := db.Db.Callback().Raw().Before("gorm:raw").Register("fail_second_drop", func(tx *gorm.DB) {
		if strings.Contains(tx.Statement.SQL.String(), "DROP TABLE") && strings.Contains(tx.Statement.SQL.String(), "sync_paths") {
			tx.AddError(errors.New("drop failed"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	err = BatchDropTable()
	db.Db.Callback().Raw().Remove("fail_second_drop")
	if err == nil {
		t.Fatal("expected partial reset error")
	}
	if err := db.Db.AutoMigrate(&SyncFile{}); err != nil {
		t.Fatal(err)
	}
	after, err := readSyncLogicalPositions(t.Context(), db.Db, sp.ID, true, "", "")
	if err != nil || len(after) != 0 {
		t.Fatalf("reset reused deleted positions: %v %v", after, err)
	}
}

func TestSyncPositionCacheChecksVersionAfterAcquire(t *testing.T) {
	sp := setupSyncScopeTest(t)
	reads := 0
	if err := db.Db.Callback().Query().After("gorm:query").Register("mutate_after_scope_acquired", func(tx *gorm.DB) {
		if tx.Statement.Table != "sync_paths" {
			return
		}
		reads++
		// 无实际等待时，取得许可后的配置重读期间仍可能发生位置提交。
		if reads == 2 {
			finish := BeginSyncPositionMutation()
			finish()
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("mutate_after_scope_acquired")
	_, release, err := AcquireSyncPathScope(t.Context(), sp.ID)
	if err != nil {
		t.Fatal(err)
	}
	release()
	if reads != 4 {
		t.Fatalf("acquired scope must retry version change: reads=%d", reads)
	}
}
