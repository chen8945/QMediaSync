//go:build integration

package models

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func optimizedSnapshotFixture(template EmbyItemSnapshot, itemID, name string) EmbyItemSnapshot {
	snapshot := template
	snapshot.Item = template.Item
	snapshot.Item.BaseModel = BaseModel{}
	snapshot.Item.ItemId = itemID
	snapshot.Item.Name = name
	snapshot.Item.SnapshotID = 0
	snapshot.Sources = append([]EmbySnapshotSource(nil), template.Sources...)
	for i := range snapshot.Sources {
		snapshot.Sources[i].ID = "source-" + itemID
		snapshot.Sources[i].ItemID = itemID
	}
	return snapshot
}

func TestEmbySnapshotOptimizedPostgresRepairsMembershipNulls(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	_, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
	testEmbySnapshotMembershipNulls(t, token, snapshot)
}

func TestEmbySnapshotOptimizedPostgresGroupsRollbackAndBound(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	_, token, snapshot := seedEmbyPostgresSnapshot(t, conn)
	second := optimizedSnapshotFixture(snapshot, "9209", "second group")
	testEmbySnapshotGroupsRollbackAndBound(t, token, snapshot, second)
}

// holdOptimizedSnapshotEvidence 暂停在真实 INSERT 成功后，尚未执行其余投影和 COMMIT。
func holdOptimizedSnapshotEvidence(t *testing.T, conn *gorm.DB, itemID string) (<-chan int, func()) {
	t.Helper()
	entered := make(chan int, 1)
	release := make(chan struct{})
	var once sync.Once
	unlock := func() { once.Do(func() { close(release) }) }
	name := "test:hold_optimized_evidence"
	if err := conn.Callback().Create().After("gorm:create").Register(name, func(tx *gorm.DB) {
		if tx.Error != nil || tx.Statement.Table != "emby_item_evidences" {
			return
		}
		evidence, ok := tx.Statement.Dest.(*EmbyItemEvidence)
		if !ok || evidence.ItemID != itemID || evidence.ItemJSON == "" {
			return
		}
		var pid int
		if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
			tx.AddError(err)
			return
		}
		select {
		case entered <- pid:
		default:
			return
		}
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unlock(); conn.Callback().Create().Remove(name) })
	return entered, unlock
}

func TestEmbySnapshotOptimizedPostgresIndependentWritesProgress(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	_, token, template := seedEmbyPostgresSnapshot(t, conn)
	first := optimizedSnapshotFixture(template, "9201", "first still uncommitted")
	second := optimizedSnapshotFixture(template, "9202", "second committed")
	entered, release := holdOptimizedSnapshotEvidence(t, conn, first.Item.ItemId)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstResult := make(chan error, 1)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		firstResult <- ApplyEmbySnapshotsContext(ctx, token, []EmbyItemSnapshot{first})
	}()
	defer func() { release(); waitEmbyPostgresFinished(t, firstDone) }()
	select {
	case <-entered:
	case err := <-firstResult:
		t.Fatalf("first group never reached successful INSERT: %v", err)
	case <-ctx.Done():
		t.Fatal("first group did not reach successful INSERT")
	}
	secondResult := make(chan error, 1)
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		secondResult <- ApplyEmbySnapshotsContext(ctx, token, []EmbyItemSnapshot{second})
	}()
	defer func() { release(); waitEmbyPostgresFinished(t, secondDone) }()
	select {
	case err := <-secondResult:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("independent group could not COMMIT while the first group held a successful INSERT")
	}
	var persisted EmbyMediaItem
	if err := conn.Where("item_id = ?", second.Item.ItemId).First(&persisted).Error; err != nil || persisted.Name != second.Item.Name {
		t.Fatalf("second group not externally visible before first COMMIT: %+v, %v", persisted, err)
	}
	var firstEvidenceCount int64
	if err := conn.Model(&EmbyItemEvidence{}).Where("item_id = ?", first.Item.ItemId).Count(&firstEvidenceCount).Error; err != nil || firstEvidenceCount != 0 {
		t.Fatalf("first group unexpectedly committed: count=%d err=%v", firstEvidenceCount, err)
	}
	release()
	if err := receiveEmbyPostgresResult(t, firstResult); err != nil {
		t.Fatal(err)
	}
}

func TestEmbySnapshotOptimizedPostgresFirstInsertSerializes(t *testing.T) {
	conn, schema := setupEmbySnapshotPostgres(t)
	_, token, template := seedEmbyPostgresSnapshot(t, conn)
	first := optimizedSnapshotFixture(template, "9203", "first")
	second := optimizedSnapshotFixture(template, "9203", "second")
	entered, release := holdOptimizedSnapshotEvidence(t, conn, first.Item.ItemId)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	firstResult := make(chan error, 1)
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		firstResult <- ApplyEmbySnapshotsContext(ctx, token, []EmbyItemSnapshot{first})
	}()
	defer func() { release(); waitEmbyPostgresFinished(t, firstDone) }()
	var firstPID int
	select {
	case firstPID = <-entered:
	case err := <-firstResult:
		t.Fatalf("first group never reached successful INSERT: %v", err)
	case <-ctx.Done():
		t.Fatal("first group did not reach successful INSERT")
	}
	secondResult := make(chan error, 1)
	secondDone := make(chan struct{})
	go func() {
		defer close(secondDone)
		secondResult <- ApplyEmbySnapshotsContext(ctx, token, []EmbyItemSnapshot{second})
	}()
	defer func() { release(); waitEmbyPostgresFinished(t, secondDone) }()
	waitEmbyPostgresLock(t, conn, schema, firstPID, secondResult)
	release()
	if err := receiveEmbyPostgresResult(t, firstResult); err != nil {
		t.Fatal(err)
	}
	if err := receiveEmbyPostgresResult(t, secondResult); err != nil {
		t.Fatal(err)
	}
	var persisted EmbyMediaItem
	if err := conn.Where("item_id = ?", first.Item.ItemId).First(&persisted).Error; err != nil || persisted.Name != "second" {
		t.Fatalf("second serialized update missing: %+v, %v", persisted, err)
	}
	for _, model := range []any{&EmbyMediaItem{}, &EmbyItemState{}, &EmbyItemMembership{}} {
		var count int64
		if err := conn.Model(model).Where("item_id = ?", first.Item.ItemId).Count(&count).Error; err != nil || count != 1 {
			t.Fatalf("first-insert identity duplicated for %T: count=%d err=%v", model, count, err)
		}
	}
}

func TestEmbySnapshotOptimizedPostgresSyncPathDeletionWaitsBeforeRootLock(t *testing.T) {
	conn, schema := setupEmbySnapshotPostgres(t)
	_, token, template := seedEmbyPostgresSnapshot(t, conn)
	if err := conn.AutoMigrate(&DirectoryUploadRule{}, &DirectoryUploadProcessedFile{}); err != nil {
		t.Fatal(err)
	}
	var root SyncPath
	if err := conn.First(&root).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := optimizedSnapshotFixture(template, "9204", "committed before root deletion")
	entered := make(chan int, 1)
	release := make(chan struct{})
	var held, released sync.Once
	unlock := func() { released.Do(func() { close(release) }) }
	const callback = "test:hold_optimized_file_prefetch"
	if err := conn.Callback().Query().After("gorm:query").Register(callback, func(tx *gorm.DB) {
		if tx.Error != nil || tx.RowsAffected == 0 || tx.Statement.Table != "sync_files" || !strings.Contains(tx.Statement.SQL.String(), "pick_code IN") {
			return
		}
		// 此时持有文件共享锁，尚未读取同步根；旧删除顺序会形成 root/file 等待环。
		held.Do(func() {
			var pid int
			if err := tx.Session(&gorm.Session{NewDB: true}).Raw("SELECT pg_backend_pid()").Scan(&pid).Error; err != nil {
				tx.AddError(err)
				return
			}
			entered <- pid
			select {
			case <-release:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		})
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unlock(); conn.Callback().Query().Remove(callback) })
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	snapshotResult, deleteResult := make(chan error, 1), make(chan error, 1)
	snapshotDone, deleteDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(snapshotDone)
		snapshotResult <- ApplyEmbySnapshotsContext(ctx, token, []EmbyItemSnapshot{snapshot})
	}()
	defer func() { unlock(); waitEmbyPostgresFinished(t, snapshotDone) }()
	var snapshotPID int
	select {
	case snapshotPID = <-entered:
	case err := <-snapshotResult:
		t.Fatalf("snapshot never held its file lock: %v", err)
	case <-ctx.Done():
		t.Fatal("snapshot did not reach file prefetch")
	}
	go func() {
		defer close(deleteDone)
		deleteResult <- DeleteSyncPathByID(ctx, root.ID)
	}()
	defer func() { unlock(); waitEmbyPostgresFinished(t, deleteDone) }()
	waitEmbyPostgresLock(t, conn, schema, snapshotPID, deleteResult)
	unlock()
	snapshotErr := receiveEmbyPostgresResult(t, snapshotResult)
	deleteErr := receiveEmbyPostgresResult(t, deleteResult)
	if snapshotErr != nil || deleteErr != nil || ctx.Err() != nil {
		t.Fatalf("snapshot and root deletion must both commit without cancellation: snapshot=%v delete=%v context=%v", snapshotErr, deleteErr, ctx.Err())
	}
	var persisted EmbyMediaItem
	if err := conn.Where("item_id = ?", snapshot.Item.ItemId).First(&persisted).Error; err != nil || persisted.SnapshotID == 0 {
		t.Fatalf("snapshot did not commit before root deletion: %+v, %v", persisted, err)
	}
	for _, model := range []any{&SyncFile{}, &EmbyMediaSyncFile{}, &EmbyLibrarySyncPath{}} {
		var count int64
		if err := conn.Model(model).Where("sync_path_id = ?", root.ID).Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("root deletion retained %T rows: count=%d err=%v", model, count, err)
		}
	}
	var rootCount int64
	if err := conn.Model(&SyncPath{}).Where("id = ?", root.ID).Count(&rootCount).Error; err != nil || rootCount != 0 {
		t.Fatalf("root deletion did not commit: count=%d err=%v", rootCount, err)
	}
	var state EmbyIndexState
	if err := conn.First(&state, 1).Error; err != nil || state.Revision != token.Revision {
		t.Fatalf("root deletion changed index revision: revision=%d want=%d err=%v", state.Revision, token.Revision, err)
	}
}
