package models

import (
	"reflect"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

var sqliteEmbyLookupIndexes = []struct {
	model any
	name  string
}{
	{&SyncFile{}, syncFileSidecarLookupIndexName},
	{&SyncFile{}, syncFileDirectoryNameIndexName},
	{&SyncFile{}, syncFileDirectoryPickCodeIndexName},
	{&EmbyMediaItem{}, embyMediaPartLookupIndexName},
	{&EmbyMediaItem{}, embyMediaVersionLookupIndexName},
}

func assertSQLiteEmbyLookupIndexes(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, index := range sqliteEmbyLookupIndexes {
		want := conn.Dialector.Name() == "sqlite"
		if conn.Migrator().HasIndex(index.model, index.name) != want {
			t.Fatalf("index %s must exist only on SQLite", index.name)
		}
		if !want {
			continue
		}
		var indexes []struct {
			Name   string
			Unique bool
		}
		if err := conn.Raw("PRAGMA index_list(" + GetTableName(index.model) + ")").Scan(&indexes).Error; err != nil {
			t.Fatal(err)
		}
		for _, got := range indexes {
			if got.Name == index.name && got.Unique {
				t.Fatalf("lookup index %s must not impose uniqueness", index.name)
			}
		}
	}
}

func assertSQLiteEmbyIndexesAbsent(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, index := range sqliteEmbyLookupIndexes {
		if conn.Migrator().HasIndex(index.model, index.name) {
			t.Fatalf("failed migration retained index %s", index.name)
		}
	}
}

func seedEmbyMemberIndexRows(t *testing.T, conn *gorm.DB) func(*gorm.DB) {
	t.Helper()
	rows := []EmbyMediaItem{
		{BaseModel: BaseModel{ID: 1}, ItemId: "member-1", ServerId: "server", PartOfItemID: "parent", VersionOfItemID: "version", Path: "/old/member-1.strm", Generation: 7},
		{BaseModel: BaseModel{ID: 2}, ItemId: "member-2", ServerId: "server", PartOfItemID: "parent", VersionOfItemID: "version", Path: "/old/member-2.strm", Generation: 9},
	}
	if err := conn.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec("INSERT INTO emby_media_items (id, item_id, server_id, part_of_item_id, version_of_item_id) VALUES (3, 'null-member', NULL, NULL, NULL)").Error; err != nil {
		t.Fatal(err)
	}
	return func(current *gorm.DB) {
		t.Helper()
		var got []EmbyMediaItem
		if err := current.Where("id IN ?", []uint{1, 2}).Order("id").Find(&got).Error; err != nil || !reflect.DeepEqual(got, rows) {
			t.Fatalf("member indexes changed historical members: got=%+v err=%v", got, err)
		}
		var nullRows int64
		if err := current.Model(&EmbyMediaItem{}).Where("id = 3 AND server_id IS NULL AND part_of_item_id IS NULL AND version_of_item_id IS NULL").Count(&nullRows).Error; err != nil || nullRows != 1 {
			t.Fatalf("member indexes changed NULL values: count=%d err=%v", nullRows, err)
		}
	}
}

func TestSQLiteEmbyLookupIndexesIndependentTables(t *testing.T) {
	for _, test := range []struct {
		name   string
		model  any
		other  any
		ensure func(*gorm.DB) error
		seed   func(*testing.T, *gorm.DB) func(*gorm.DB)
	}{
		{"sync_files", &SyncFile{}, &EmbyMediaItem{}, EnsureEmbySyncFileLookupIndexes, seedSyncFileNameIndexRows},
		{"emby_media_items", &EmbyMediaItem{}, &SyncFile{}, EnsureEmbyMediaLookupIndexes, seedEmbyMemberIndexRows},
	} {
		t.Run(test.name, func(t *testing.T) {
			setupFreshInitializationSQLite(t)
			if err := test.ensure(db.Db); err != nil || db.Db.Migrator().HasTable(test.model) {
				t.Fatalf("index helper must not create an absent table: %v", err)
			}
			if err := db.Db.AutoMigrate(test.model); err != nil {
				t.Fatal(err)
			}
			assertRows := test.seed(t, db.Db)
			for range 2 {
				if err := test.ensure(db.Db); err != nil {
					t.Fatal(err)
				}
				for _, index := range sqliteEmbyLookupIndexes {
					want := GetTableName(index.model) == test.name
					if db.Db.Migrator().HasIndex(index.model, index.name) != want {
						t.Fatalf("independent table omitted or created unrelated index %s", index.name)
					}
				}
				if db.Db.Migrator().HasTable(test.other) {
					t.Fatal("index helper created an unrelated table")
				}
				assertRows(db.Db)
			}
		})
	}
}

func TestSQLiteEmbyLookupIndexesFreshFailureAndRetry(t *testing.T) {
	for _, index := range sqliteEmbyLookupIndexes {
		t.Run(index.name, func(t *testing.T) {
			reopen := setupFreshInitializationSQLite(t)
			testFreshInitializationFailure(t, db.Db, reopen, freshInitializationFailure{
				name: index.name, operation: "raw", sql: "CREATE INDEX IF NOT EXISTS " + index.name,
			})
		})
	}
}

func TestSQLiteEmbyLookupIndexesRepairAndRetry(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	assertFiles := seedSyncFileNameIndexRows(t, db.Db)
	assertMembers := seedEmbyMemberIndexRows(t, db.Db)
	for _, index := range sqliteEmbyLookupIndexes {
		if err := db.Db.Migrator().DropIndex(index.model, index.name); err != nil {
			t.Fatal(err)
		}
	}
	remove, injected, _ := failFreshInitialization(t, db.Db, freshInitializationFailure{
		name: "member_index", operation: "raw", sql: "CREATE INDEX IF NOT EXISTS " + embyMediaVersionLookupIndexName,
	})
	if err := BatchCreateTable(); err == nil || !*injected {
		t.Fatalf("repair did not propagate index failure: reached=%v err=%v", *injected, err)
	}
	remove()
	assertFiles(db.Db)
	assertMembers(db.Db)
	for range 2 {
		if err := BatchCreateTable(); err != nil {
			t.Fatal(err)
		}
		assertSQLiteEmbyLookupIndexes(t, db.Db)
		assertFiles(db.Db)
		assertMembers(db.Db)
		assertEmbyMigrationVersion(t, db.Db, MaxVersionCode)
	}
}

func TestSQLiteEmbyLookupIndexesStartAtVersion67(t *testing.T) {
	setupFreshInitializationSQLite(t)
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	for _, index := range sqliteEmbyLookupIndexes {
		if err := db.Db.Migrator().DropIndex(index.model, index.name); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Db.Migrator().DropTable(&SyncRootEvidence{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&Migrator{}).Where("1 = 1").UpdateColumn("version_code", 64).Error; err != nil {
		t.Fatal(err)
	}
	_, injected, _ := failFreshInitialization(t, db.Db, freshInitializationFailure{
		name: "version67_table", operation: "raw", sql: "CREATE TABLE", model: &SyncRootEvidence{},
	})
	if err := Migrate(); err == nil || !*injected {
		t.Fatalf("must reach and stop at version 67: reached=%v err=%v", *injected, err)
	}
	assertEmbyMigrationVersion(t, db.Db, 67)
	assertSQLiteEmbyIndexesAbsent(t, db.Db)
}
