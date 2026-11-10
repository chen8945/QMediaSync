package models

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyIdentityMigrationSQLite(t *testing.T) {
	testEmbyIdentityMigration(t, func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		reopen := setupFreshInitializationSQLite(t)
		return db.Db, reopen
	})
}

func testEmbyIdentityMigration(t *testing.T, setup func(*testing.T) (*gorm.DB, func() *gorm.DB)) {
	t.Helper()
	for _, version := range []int{65, 67, 70} {
		t.Run(fmt.Sprintf("schema_%d", version), func(t *testing.T) {
			conn, reopen := setup(t)
			seedEmbyIdentityMigration(t, conn, version)
			if err := Migrate(); err != nil {
				t.Fatal(err)
			}
			conn = reopen()
			assertEmbyMigrationVersion(t, conn, MaxVersionCode)
			var links []struct {
				ID         uint
				EmbyItemID sql.NullString
				PickCode   string
			}
			if err := conn.Table("emby_media_sync_files").Order("id").Find(&links).Error; err != nil {
				t.Fatal(err)
			}
			if len(links) != 5 || links[0].EmbyItemID.String != "00077" || links[1].EmbyItemID.String != "9007199254740993" || links[2].EmbyItemID.Valid || links[0].PickCode != "preserved" || links[3].EmbyItemID.String != "91" || links[4].EmbyItemID.String != "0092" {
				t.Fatalf("identity conversion lost data: %+v", links)
			}
			if !conn.Migrator().HasIndex(&EmbyMediaSyncFile{}, "identity_custom_index") {
				t.Fatal("lost existing index")
			}
			next := EmbyMediaSyncFile{EmbyItemId: "aabbccddaabbccddaabbccddaabbccdd"}
			if err := conn.Create(&next).Error; err != nil {
				t.Fatal(err)
			}
			if next.ID <= 1000 {
				t.Fatalf("sequence high water was reset: %d", next.ID)
			}
			if err := Migrate(); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, version := range []int{12, 15} {
		t.Run(fmt.Sprintf("historical_entry_%d", version), func(t *testing.T) {
			conn, reopen := setup(t)
			if err := Migrate(); err != nil {
				t.Fatal(err)
			}
			createLegacyEmbyLinkTable(t, conn)
			if version == 12 {
				if err := conn.Migrator().DropTable(&legacyEmbyMediaSyncFile{}); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := conn.Create(&SyncFile{BaseModel: BaseModel{ID: 7}, SyncPathId: 42}).Error; err != nil {
					t.Fatal(err)
				}
				if err := conn.Create(&legacyEmbyMediaSyncFile{EmbyItemId: 77, SyncFileId: 7}).Error; err != nil {
					t.Fatal(err)
				}
				fillSyncPathIdInEmbyMediaSyncFile(conn)
				var link legacyEmbyMediaSyncFile
				if err := conn.First(&link).Error; err != nil || link.EmbyItemId != 77 || link.SyncPathId != 42 {
					t.Fatalf("legacy directory backfill changed identity: %+v %v", link, err)
				}
			}
			if err := conn.Model(&Migrator{}).Where("id = ?", 1).Update("version_code", version).Error; err != nil {
				t.Fatal(err)
			}
			if err := Migrate(); err != nil {
				t.Fatal(err)
			}
			conn = reopen()
			assertEmbyMigrationVersion(t, conn, MaxVersionCode)
			var links int64
			if err := conn.Model(&EmbyMediaSyncFile{}).Count(&links).Error; err != nil || links != 0 {
				t.Fatalf("historical 16→17 cleanup changed: %d %v", links, err)
			}
		})
	}
	for _, failure := range []string{"conflict", "version"} {
		t.Run(failure, func(t *testing.T) {
			conn, reopen := setup(t)
			seedEmbyIdentityMigration(t, conn, 70)
			var remove func()
			if failure == "conflict" {
				if err := conn.Create(&EmbyMediaItem{ItemId: "other-77", ItemIdInt: 77}).Error; err != nil {
					t.Fatal(err)
				}
			} else {
				remove = failEmbyMigrationVersionUpdate(t, conn)
			}
			if err := Migrate(); err == nil {
				t.Fatal("migration unexpectedly succeeded")
			}
			assertEmbyMigrationVersion(t, conn, 70)
			columns, err := conn.Migrator().ColumnTypes(&legacyEmbyMediaSyncFile{})
			if err != nil {
				t.Fatal(err)
			}
			for _, column := range columns {
				if column.Name() == "emby_item_id" && !strings.Contains(strings.ToLower(column.DatabaseTypeName()), "int") {
					t.Fatal("failed migration changed integer column")
				}
			}
			if remove != nil {
				remove()
			} else if err := conn.Where("item_id = ?", "other-77").Delete(&EmbyMediaItem{}).Error; err != nil {
				t.Fatal(err)
			}
			conn = reopen()
			assertEmbyMigrationVersion(t, conn, 70)
			if err := Migrate(); err != nil {
				t.Fatal(err)
			}
			assertEmbyMigrationVersion(t, conn, MaxVersionCode)
		})
	}
}

func seedEmbyIdentityMigration(t *testing.T, conn *gorm.DB, version int) {
	t.Helper()
	if err := conn.AutoMigrate(&Migrator{}, &Settings{}, &EmbyMediaItem{}, &legacyEmbyMediaSyncFile{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&Migrator{VersionCode: version}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&EmbyMediaItem{ItemId: "00077", ItemIdInt: 77}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Table("emby_media_items").Create([]map[string]any{
		{"item_id": nil, "item_id_int": 91},
		{"item_id": nil, "item_id_int": 92},
		{"item_id": "0092", "item_id_int": 92},
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(`INSERT INTO emby_media_sync_files (id, emby_item_id, pick_code) VALUES (1, 77, 'preserved'), (2, 9007199254740993, 'orphan'), (3, NULL, 'null'), (4, 91, 'null-media'), (5, 92, 'mapped-media'), (1000, 88, 'deleted')`).Error; err != nil {
		t.Fatal(err)
	}
	if conn.Dialector.Name() == "postgres" {
		if err := conn.Transaction(func(tx *gorm.DB) error {
			return ResetSequenceTx(tx, "emby_media_sync_files", "id")
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.Exec(`DELETE FROM emby_media_sync_files WHERE id = 1000`).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Exec(`CREATE INDEX identity_custom_index ON emby_media_sync_files (pick_code, sync_path_id)`).Error; err != nil {
		t.Fatal(err)
	}
}
