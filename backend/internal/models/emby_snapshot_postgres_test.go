//go:build integration

package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func setupEmbySnapshotPostgres(t *testing.T) (*gorm.DB, string) {
	t.Helper()
	dsn := os.Getenv("QMS_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires QMS_TEST_POSTGRES_DSN for an isolated PostgreSQL test database")
	}
	parsed, err := url.Parse(dsn)
	if err != nil || parsed.User == nil || parsed.Host == "" {
		t.Fatal("QMS_TEST_POSTGRES_DSN must be a PostgreSQL URL")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	adminSQL, err := admin.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { adminSQL.Close() })
	schema := fmt.Sprintf("qms_emby_snapshot_%d_%d", os.Getpid(), time.Now().UnixNano())
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := admin.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	// 通过连接参数隔离整个连接池；SET search_path 只能约束执行它的单个连接。
	query := parsed.Query()
	query.Set("search_path", schema)
	query.Set("application_name", schema)
	query.Set("statement_timeout", "15000")
	parsed.RawQuery = query.Encode()
	conn, err := gorm.Open(postgres.Open(parsed.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(4)
	sqlDB.SetMaxIdleConns(4)
	previousDB, previousLogger := db.Db, helpers.AppLogger
	ctx, cancel := context.WithCancel(t.Context())
	conn = conn.WithContext(ctx)
	db.Db = conn
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	t.Cleanup(func() {
		cancel()
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
		db.Db, helpers.AppLogger = previousDB, previousLogger
	})
	return conn, schema
}

func seedEmbyPostgresSnapshot(t *testing.T, conn *gorm.DB) (*EmbyConfig, EmbyIndexToken, EmbyItemSnapshot) {
	t.Helper()
	if err := conn.AutoMigrate(&EmbyConfig{}, &Account{}, &SyncPath{}, &SyncFile{}, &EmbyLibrarySyncPath{}); err != nil {
		t.Fatal(err)
	}
	if err := MigrateEmbyDeletionSchema(conn); err != nil {
		t.Fatal(err)
	}
	config := &EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "isolated-test-key", SyncEnabled: 1}
	account := Account{SourceType: SourceType115, UserId: "isolated-user"}
	if err := conn.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := SyncPath{SourceType: SourceType115, AccountId: account.ID, BaseCid: "root", LocalPath: "/local", RemotePath: "/media"}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	file := SyncFile{SourceType: SourceType115, AccountId: account.ID, SyncPathId: root.ID, FileId: "video-1", ParentId: "root", FileName: "movie.mkv", Path: "/media", LocalFilePath: "/local/media/movie.strm", PickCode: "pick-1", Sha1: "generation-1", FileSize: 42, IsVideo: true}
	if err := conn.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := EmbyItemSnapshot{
		Item:    EmbyMediaItem{ItemId: "9101", Name: "original", Type: "Movie", LibraryId: "100", Path: file.LocalFilePath, DateCreated: "2026-10-01T00:00:00Z", LastSeenSyncRun: "old-run"},
		Sources: []EmbySnapshotSource{{ID: "source-1", ItemID: "9101", Path: "http://media.invalid/video", PickCode: file.PickCode}},
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	return config, token, snapshot
}

func TestEmbySnapshotPostgresDeletionSerializesWithSnapshot(t *testing.T) {
	for _, operation := range []string{"apply", "cleanup"} {
		t.Run(operation, func(t *testing.T) {
			conn, schema := setupEmbySnapshotPostgres(t)
			_, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
			tx := conn.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			defer tx.Rollback()
			var blockerPID int
			if err := tx.Raw("SELECT pg_backend_pid()").Scan(&blockerPID).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := RegisterEmbyDeletionTx(tx, token.ServerID, []string{snapshot.Item.ItemId}); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			finished := make(chan struct{})
			t.Cleanup(func() { waitEmbyPostgresFinished(t, finished) })
			go func() {
				defer close(finished)
				if operation == "cleanup" {
					result <- CleanupEmbyLibrarySnapshot(token, "100", "new-run")
					return
				}
				snapshot.Item.Name = "stale overwrite"
				result <- ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot})
			}()
			waitEmbyPostgresLock(t, conn, schema, blockerPID, result)
			if err := tx.Commit().Error; err != nil {
				t.Fatal(err)
			}
			if err := receiveEmbyPostgresResult(t, result); !errors.Is(err, ErrEmbySnapshotStale) {
				t.Fatalf("delete commit must reject stale %s: %v", operation, err)
			}
			var item EmbyMediaItem
			if err := conn.Where("item_id = ?", "9101").First(&item).Error; err != nil || item.Name != "original" {
				t.Fatalf("stale operation changed current index: %+v, %v", item, err)
			}
			assertEmbyPostgresCount(t, conn, &EmbyMediaSyncFile{}, 1)
			assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 1)
		})
	}
}

func TestEmbySnapshotPostgresSnapshotSerializesWithDeletion(t *testing.T) {
	conn, schema := setupEmbySnapshotPostgres(t)
	_, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
	held := make(chan int, 1)
	release := make(chan struct{})
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	if err := conn.Callback().Create().Before("gorm:create").Register("test:hold_emby_snapshot", func(tx *gorm.DB) {
		if tx.Statement.Table != conn.NamingStrategy.TableName("EmbyItemEvidence") {
			return
		}
		var pid int
		if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			tx.AddError(err)
			return
		}
		held <- pid
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Create().Remove("test:hold_emby_snapshot") })
	snapshot.Item.Name = "committed before deletion"
	applyResult := make(chan error, 1)
	applyFinished := make(chan struct{})
	t.Cleanup(func() { waitEmbyPostgresFinished(t, applyFinished) })
	go func() {
		defer close(applyFinished)
		applyResult <- ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot})
	}()
	var blockerPID int
	select {
	case blockerPID = <-held:
	case err := <-applyResult:
		t.Fatalf("snapshot did not reach the paused commit: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("snapshot did not reach evidence creation")
	}
	deleteResult := make(chan error, 1)
	deleteFinished := make(chan struct{})
	t.Cleanup(func() { waitEmbyPostgresFinished(t, deleteFinished) })
	go func() {
		defer close(deleteFinished)
		deleteResult <- conn.Transaction(func(tx *gorm.DB) error {
			_, err := RegisterEmbyDeletionTx(tx, token.ServerID, []string{"9101"})
			return err
		})
	}()
	waitEmbyPostgresLock(t, conn, schema, blockerPID, deleteResult)
	close(release)
	released = true
	if err := receiveEmbyPostgresResult(t, applyResult); err != nil {
		t.Fatalf("snapshot holding the lock must finish first: %v", err)
	}
	if err := receiveEmbyPostgresResult(t, deleteResult); err != nil {
		t.Fatal(err)
	}
	var state EmbyItemState
	if err := conn.Where("server_id = ? AND item_id = ?", token.ServerID, "9101").First(&state).Error; err != nil || !state.Deleted {
		t.Fatalf("later deletion barrier was overwritten by snapshot commit: %+v, %v", state, err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("later deletion did not invalidate the old token: %v", err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 2)
}

// PostgreSQL 自身报告锁等待后才释放事务，不能用睡眠推测写入是否已经开始。
func waitEmbyPostgresLock(t *testing.T, conn *gorm.DB, schema string, blockerPID int, result <-chan error) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-result:
			t.Fatalf("concurrent operation finished without waiting for the other transaction: %v", err)
		case <-deadline.C:
			t.Fatal("PostgreSQL did not report the expected row-lock wait")
		case <-tick.C:
			var blocked bool
			if err := conn.Raw(`SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE application_name = ? AND wait_event_type = 'Lock'
				AND ? = ANY(pg_blocking_pids(pid)))`, schema, blockerPID).Scan(&blocked).Error; err != nil {
				t.Fatal(err)
			}
			if blocked {
				return
			}
		}
	}
}

func receiveEmbyPostgresResult(t *testing.T, result <-chan error) error {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case err := <-result:
		return err
	case <-timer.C:
		t.Fatal("concurrent database operation did not finish")
		return nil
	}
}

func waitEmbyPostgresFinished(t *testing.T, finished <-chan struct{}) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-finished:
	case <-timer.C:
		t.Error("concurrent database operation did not stop before cleanup")
	}
}

func TestEmbySnapshotPostgresUnknownDeletionAndAdmission(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	config, oldToken, snapshot := seedEmbyPostgresSnapshot(t, conn)
	snapshot.Item.ItemId = "9102"
	snapshot.Sources[0].ItemID = "9102"
	var revision int64
	if err := conn.Transaction(func(tx *gorm.DB) (err error) {
		revision, err = RegisterEmbyDeletionTx(tx, oldToken.ServerID, []string{"9102"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(oldToken, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("unknown deleted item accepted old snapshot: %v", err)
	}
	deletedToken, err := BeginEmbyIndexRead(oldToken.ServerID, config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(deletedToken, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbyItemDeleted) {
		t.Fatalf("new read must not clear unknown item's deletion barrier: %v", err)
	}
	if err := conn.Transaction(func(tx *gorm.DB) error {
		return AdmitEmbyItemGenerationTx(tx, oldToken.ServerID, "9102", revision-1)
	}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("wrong deletion revision admitted: %v", err)
	}
	if err := conn.Transaction(func(tx *gorm.DB) error {
		return AdmitEmbyItemGenerationTx(tx, oldToken.ServerID, "9102", revision)
	}); err != nil {
		t.Fatal(err)
	}
	current, err := BeginEmbyIndexRead(oldToken.ServerID, config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Item.Name = "new generation"
	if err := ApplyEmbySnapshots(current, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	for _, token := range []EmbyIndexToken{oldToken, deletedToken} {
		if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
			t.Fatalf("admission revived an old read token: %v", err)
		}
	}
	assertEmbyPostgresCount(t, conn, &EmbyMediaItem{}, 2)
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 2)
}

func TestEmbySnapshotPostgresNewGenerationNeverRevivesOldToken(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	config, oldToken, snapshot := seedEmbyPostgresSnapshot(t, conn)
	var oldState EmbyItemState
	if err := conn.Where("item_id = ?", "9101").First(&oldState).Error; err != nil {
		t.Fatal(err)
	}
	var deletionRevision int64
	if err := conn.Transaction(func(tx *gorm.DB) (err error) {
		deletionRevision, err = RegisterEmbyDeletionTx(tx, oldToken.ServerID, []string{"9101"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Transaction(func(tx *gorm.DB) error {
		return AdmitEmbyItemGenerationTx(tx, oldToken.ServerID, "9101", deletionRevision)
	}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&SyncFile{}).Where("file_id = ?", "video-1").Updates(map[string]any{"file_id": "replacement", "sha1": "generation-2"}).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead(oldToken.ServerID, config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Item.Name = "replacement at same path"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(oldToken, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("admitting a replacement revived an old read: %v", err)
	}
	var state EmbyItemState
	if err := conn.First(&state, oldState.ID).Error; err != nil || state.Generation <= oldState.Generation || state.SnapshotID == oldState.SnapshotID || state.Deleted {
		t.Fatalf("new lifecycle was not independently recorded: old=%+v new=%+v, %v", oldState, state, err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 2)
}

func TestEmbySnapshotPostgresConfigChangesRejectOldReads(t *testing.T) {
	for _, change := range []string{"server", "url", "key", "disabled"} {
		t.Run(change, func(t *testing.T) {
			conn, _ := setupEmbySnapshotPostgres(t)
			config, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
			if change == "server" {
				if _, err := BeginEmbyIndexRead("server-b", config); err != nil {
					t.Fatal(err)
				}
			} else {
				fields := map[string]any{}
				switch change {
				case "url":
					fields["emby_url"] = "http://another.invalid"
				case "key":
					fields["emby_api_key"] = "another-isolated-key"
				case "disabled":
					fields["sync_enabled"] = 0
				}
				if err := conn.Model(config).Updates(fields).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
				t.Fatalf("changed %s accepted old snapshot: %v", change, err)
			}
			if err := CleanupEmbyLibrarySnapshot(token, "100", "new-run"); !errors.Is(err, ErrEmbySnapshotStale) {
				t.Fatalf("changed %s accepted old cleanup: %v", change, err)
			}
			assertEmbyPostgresCount(t, conn, &EmbyMediaItem{}, 1)
		})
	}
}

func TestEmbySnapshotPostgresCleanupRetainsFrozenEvidence(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	_, token, _ := seedEmbyPostgresSnapshot(t, conn)
	var before EmbyItemEvidence
	if err := conn.First(&before).Error; err != nil {
		t.Fatal(err)
	}
	var files []EmbyFrozenFile
	if err := json.Unmarshal([]byte(before.FilesJSON), &files); err != nil || len(files) != 1 || files[0].Reason != "" || files[0].FileID != "video-1" {
		t.Fatalf("fixture did not freeze a verified physical identity: %+v, %v", files, err)
	}
	if err := CleanupEmbyLibrarySnapshot(token, "100", "new-run"); err != nil {
		t.Fatal(err)
	}
	assertEmbyPostgresCount(t, conn, &EmbyMediaItem{}, 0)
	assertEmbyPostgresCount(t, conn, &EmbyMediaSyncFile{}, 0)
	assertEmbyPostgresCount(t, conn, &EmbyItemState{}, 1)
	assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 1)
	var after EmbyItemEvidence
	if err := conn.First(&after, before.ID).Error; err != nil || after != before {
		t.Fatalf("index cleanup changed deletion evidence: %+v, %v", after, err)
	}
}

func assertEmbyPostgresCount(t *testing.T, conn *gorm.DB, model any, want int64) {
	t.Helper()
	var got int64
	if err := conn.Model(model).Count(&got).Error; err != nil || got != want {
		t.Fatalf("%T count = %d, want %d: %v", model, got, want, err)
	}
}
