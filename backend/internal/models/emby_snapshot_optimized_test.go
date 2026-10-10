package models

import (
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbySnapshotOptimizedPreserveRawProjectionAndRepairDerivedRows(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.LastSeenSyncRun, snapshot.Item.LastSeenAt = "full-before", 100
	snapshot.Item.MediaSourcePath += "&token=before"
	snapshot.Sources[0].Path += "&token=before"
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var before EmbyMediaItem
	if err := db.Db.First(&before).Error; err != nil {
		t.Fatal(err)
	}

	snapshot.Item.LastSeenSyncRun, snapshot.Item.LastSeenAt = "full-after", 200
	snapshot.Item.MediaSourcePath = "http://qms/stream?pickcode=" + file.PickCode + "&token=after"
	snapshot.Sources[0].Path = snapshot.Item.MediaSourcePath
	// NULL 派生值也必须被修复，不能被 SQL 三值逻辑当作相同。
	if err := db.Db.Model(&EmbyItemMembership{}).Where("item_id = ?", snapshot.Item.ItemId).Update("season_id", nil).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var after EmbyMediaItem
	if err := db.Db.First(&after).Error; err != nil {
		t.Fatal(err)
	}
	if before.SnapshotID != after.SnapshotID || before.Generation != after.Generation || before.CreatedAt != after.CreatedAt {
		t.Fatalf("unchanged evidence changed: before=%+v after=%+v", before, after)
	}
	if after.MediaSourcePath != snapshot.Item.MediaSourcePath || after.LastSeenSyncRun != "full-after" || after.LastSeenAt != 200 {
		t.Fatalf("raw fields/full markers stale: %+v", after)
	}
	var nullCount int64
	if err := db.Db.Model(&EmbyItemMembership{}).Where("season_id IS NULL").Count(&nullCount).Error; err != nil {
		t.Fatal(err)
	}
	if nullCount != 0 {
		t.Fatal("NULL membership was not repaired")
	}

	for _, model := range []any{&EmbyMediaItem{}, &EmbyMediaSyncFile{}, &EmbyItemMembership{}} {
		if err := db.Db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(model).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []any{&EmbyMediaItem{}, &EmbyMediaSyncFile{}, &EmbyItemMembership{}, &EmbyItemEvidence{}, &EmbyItemState{}} {
		var count int64
		if err := db.Db.Model(model).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("repair count for %T = %d, want 1", model, count)
		}
	}
	var repaired EmbyMediaItem
	if err := db.Db.First(&repaired).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.SnapshotID != before.SnapshotID {
		t.Fatal("repair created replacement evidence")
	}
}

func TestEmbySnapshotOptimizedSkipUnchangedProjectionWrites(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.LastSeenSyncRun, snapshot.Item.LastSeenAt = "same-full", 100
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var changed int64
	const callback = "test:optimized_changed_projection"
	inspect := func(tx *gorm.DB) {
		switch tx.Statement.Table {
		case "emby_media_items", "emby_media_sync_files", "emby_item_states", "emby_item_memberships", "emby_item_evidences", "emby_library_sync_paths":
			changed += tx.RowsAffected
		}
	}
	if err := db.Db.Callback().Create().After("gorm:create").Register(callback, inspect); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Update().After("gorm:update").Register(callback, inspect); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Delete().After("gorm:delete").Register(callback, inspect); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Create().Remove(callback)
	defer db.Db.Callback().Update().Remove(callback)
	defer db.Db.Callback().Delete().Remove(callback)
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("unchanged projections changed %d rows", changed)
	}
}

func TestEmbySnapshotOptimizedIncrementalPreservesNullFullMarker(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", snapshot.Item.ItemId).UpdateColumn("last_seen_sync_run", nil).Error; err != nil {
		t.Fatal(err)
	}
	snapshot.Item.Name, snapshot.Item.LastSeenAt = "incremental change", 200
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var after struct {
		Name            string
		LastSeenAt      int64
		LastSeenSyncRun *string
	}
	if err := db.Db.Model(&EmbyMediaItem{}).Where("item_id = ?", snapshot.Item.ItemId).Take(&after).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != snapshot.Item.Name || after.LastSeenAt != 200 || after.LastSeenSyncRun != nil {
		t.Fatalf("incremental update changed the unowned full marker: %+v", after)
	}
}

func TestEmbySnapshotOptimizedPrefetchPreservesGlobalItemIdentity(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	old := EmbyMediaItem{ItemId: "101", ServerId: "previous-server", Name: "old index"}
	foreignMember := EmbyMediaItem{ItemId: "102", ServerId: "previous-server", PartOfItemID: "101", VersionOfItemID: "101"}
	for _, item := range []*EmbyMediaItem{&old, &foreignMember} {
		if err := db.Db.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	snapshot := snapshotForFile(file)
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var after EmbyMediaItem
	if err := db.Db.First(&after, old.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.ItemId != old.ItemId || after.ServerId != token.ServerID || after.CreatedAt != old.CreatedAt {
		t.Fatalf("global item identity was not reused: %+v", after)
	}
	var memberAfter EmbyMediaItem
	if err := db.Db.First(&memberAfter, foreignMember.ID).Error; err != nil {
		t.Fatal(err)
	}
	if memberAfter != foreignMember {
		t.Fatalf("complete group modified another server's member: before=%+v after=%+v", foreignMember, memberAfter)
	}
}

func TestEmbySnapshotOptimizedRepairsMembershipNulls(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	testEmbySnapshotMembershipNulls(t, token, snapshotForFile(file))
}

func testEmbySnapshotMembershipNulls(t *testing.T, token EmbyIndexToken, snapshot EmbyItemSnapshot) {
	t.Helper()
	if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var before EmbyItemMembership
	if err := db.Db.Where("server_id = ? AND item_id = ?", token.ServerID, snapshot.Item.ItemId).First(&before).Error; err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"server_config_key", "snapshot_id", "item_type", "part_of_item_id", "season_id", "series_id"} {
		t.Run(column, func(t *testing.T) {
			if err := db.Db.Model(&EmbyItemMembership{}).Where("id = ?", before.ID).UpdateColumn(column, nil).Error; err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshotsContext(t.Context(), token, []EmbyItemSnapshot{snapshot}); err != nil {
				t.Fatal(err)
			}
			var after EmbyItemMembership
			if err := db.Db.First(&after, before.ID).Error; err != nil {
				t.Fatal(err)
			}
			beforeValue, afterValue := before, after
			beforeValue.BaseModel, afterValue.BaseModel = BaseModel{}, BaseModel{}
			if beforeValue != afterValue || before.CreatedAt != after.CreatedAt {
				t.Fatalf("membership repair changed evidence or identity: before=%+v after=%+v", before, after)
			}
			var nulls int64
			if err := db.Db.Model(&EmbyItemMembership{}).Where("id = ? AND "+column+" IS NULL", before.ID).Count(&nulls).Error; err != nil {
				t.Fatal(err)
			}
			if nulls != 0 {
				t.Fatal("NULL membership was skipped instead of repaired")
			}
		})
	}
}
