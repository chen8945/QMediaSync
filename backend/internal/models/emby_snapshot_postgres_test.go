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
	"reflect"
	"strings"
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
	if err := MigrateEmbySnapshots(conn); err != nil {
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

func TestEmbySnapshotPostgresFreshSchema(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	if err := BatchCreateTable(); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&EmbyIndexState{}, &EmbyItemState{}, &EmbyItemEvidence{}} {
		if !conn.Migrator().HasTable(model) {
			t.Fatalf("fresh schema omitted %T", model)
		}
	}
	if !conn.Migrator().HasIndex(&EmbyItemState{}, "idx_emby_item_state") {
		t.Fatal("fresh schema omitted unique server/item state index")
	}
}

func TestEmbySnapshotPostgresMigrationPreservesLegacyAndRetries(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	var migrationLog strings.Builder
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&migrationLog, "", 0)}
	defer func() {
		if t.Failed() {
			t.Log(migrationLog.String())
		}
	}()
	if err := conn.AutoMigrate(&Migrator{}, &EmbyMediaItem{}, &EmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"PartCount", "PartOfItemID", "VersionOfItemID", "SnapshotID", "Generation"} {
		if err := conn.Migrator().DropColumn(&EmbyMediaItem{}, field); err != nil {
			t.Fatal(err)
		}
	}
	for _, field := range []string{"SnapshotID", "SourceID"} {
		if err := conn.Migrator().DropColumn(&EmbyMediaSyncFile{}, field); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Create(&Migrator{VersionCode: 65}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(`INSERT INTO emby_media_items
		(id, created_at, updated_at, item_id, item_id_int, server_id, name, path, pick_code, last_seen_sync_run)
		VALUES (7, 11, 12, '9101', 9101, '', 'legacy', '/local/movie.strm', 'old-pick', 'old-run')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(`INSERT INTO emby_media_sync_files
		(id, created_at, updated_at, emby_item_id, sync_file_id, sync_path_id, pick_code)
		VALUES (3, 13, 14, 9101, 29, 41, 'old-pick')`).Error; err != nil {
		t.Fatal(err)
	}
	// 在后续证据表建表时注入错误，模拟前面字段已补齐后的迁移中断。
	injected, failMigration := false, true
	if err := conn.Callback().Raw().Before("gorm:raw").Register("test:emby_snapshot_migration_failure", func(tx *gorm.DB) {
		query := tx.Statement.SQL.String()
		if failMigration && strings.Contains(query, "CREATE TABLE") && strings.Contains(query, conn.NamingStrategy.TableName("EmbyItemEvidence")) {
			injected = true
			tx.AddError(errors.New("injected evidence table creation failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Raw().Remove("test:emby_snapshot_migration_failure") })
	Migrate()
	if !injected {
		t.Fatal("migration did not reach the injected evidence table failure")
	}
	var version Migrator
	if err := conn.First(&version).Error; err != nil || version.VersionCode != 65 {
		t.Fatalf("failed migration advanced schema version: %+v, %v", version, err)
	}
	if conn.Migrator().HasColumn(&EmbyMediaItem{}, "SnapshotID") || conn.Migrator().HasTable(&EmbyItemState{}) {
		t.Fatal("failed migration did not roll back preceding DDL")
	}
	failMigration = false
	var firstItem EmbyMediaItem
	var firstLink EmbyMediaSyncFile
	for attempt := range 2 {
		Migrate()
		if err := conn.First(&version).Error; err != nil || version.VersionCode != MaxVersionCode {
			t.Fatalf("migration retry failed: %+v, %v", version, err)
		}
		var item EmbyMediaItem
		var link EmbyMediaSyncFile
		if err := conn.First(&item, 7).Error; err != nil {
			t.Fatal(err)
		}
		if err := conn.First(&link, 3).Error; err != nil {
			t.Fatal(err)
		}
		if item.ID != 7 || item.CreatedAt != 11 || item.UpdatedAt != 12 || item.ItemId != "9101" || item.ItemIdInt != 9101 || item.ServerId != "" || item.Name != "legacy" || item.Path != "/local/movie.strm" || item.PickCode != "old-pick" || item.LastSeenSyncRun != "old-run" || item.SnapshotID != 0 || item.Generation != 0 {
			t.Fatalf("migration rewrote legacy item or promoted it to trusted evidence: %+v", item)
		}
		if link.ID != 3 || link.CreatedAt != 13 || link.UpdatedAt != 14 || link.EmbyItemId != 9101 || link.SyncFileId != 29 || link.SyncPathId != 41 || link.PickCode != "old-pick" || link.SnapshotID != 0 || link.SourceID != "" {
			t.Fatalf("migration rewrote legacy association: %+v", link)
		}
		if attempt == 0 {
			firstItem, firstLink = item, link
		} else if !reflect.DeepEqual(item, firstItem) || !reflect.DeepEqual(link, firstLink) {
			t.Fatal("repeated startup changed migrated rows")
		}
		assertEmbyPostgresCount(t, conn, &EmbyItemEvidence{}, 0)
		assertEmbyPostgresCount(t, conn, &EmbyItemState{}, 0)
	}
}

func assertEmbyPostgresCount(t *testing.T, conn *gorm.DB, model any, want int64) {
	t.Helper()
	var got int64
	if err := conn.Model(model).Count(&got).Error; err != nil || got != want {
		t.Fatalf("%T count = %d, want %d: %v", model, got, want, err)
	}
}
