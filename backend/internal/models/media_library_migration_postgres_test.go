//go:build integration

package models

import (
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestMediaLibraryMigrationPostgres(t *testing.T) {
	testMediaLibraryMigration(t, func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		conn, _ := setupEmbySnapshotPostgres(t)
		return conn, func() *gorm.DB {
			sqlDB, err := conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			if err := sqlDB.Close(); err != nil {
				t.Fatal(err)
			}
			conn, err = gorm.Open(conn.Dialector, &gorm.Config{Logger: conn.Logger})
			if err != nil {
				t.Fatal(err)
			}
			sqlDB, err = conn.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB.Close() })
			db.Db = conn
			return conn
		}
	})
}

func TestMediaLibraryEvidenceIsolationPostgres(t *testing.T) {
	conn, _ := setupEmbySnapshotPostgres(t)
	testMediaLibraryEvidenceIsolation(t, conn)
}

func TestMediaLibraryQueriesIsolationPostgres(t *testing.T) {
	for _, cleanup := range []string{"all", "unselected", "stale"} {
		t.Run(cleanup, func(t *testing.T) {
			conn, _ := setupEmbySnapshotPostgres(t)
			if err := conn.AutoMigrate(&EmbyMediaItem{}, &EmbyMediaSyncFile{}, &EmbyLibrary{}, &EmbyLibrarySyncPath{}); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
				if err := CreateOrUpdateEmbyMediaItem(kind, &EmbyMediaItem{ItemId: "101", LibraryId: "lib", LastSeenSyncRun: "old"}); err != nil {
					t.Fatal(err)
				}
				if err := CreateEmbyMediaSyncFile(kind, "101", 1, "pick", 1); err != nil {
					t.Fatal(err)
				}
				if err := conn.Create(&EmbyLibrary{MediaServerType: kind, LibraryId: "lib"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			var err error
			switch cleanup {
			case "all":
				err = CleanupAllEmbyLibraryData(MediaServerEmby)
			case "unselected":
				err = CleanupUnselectedEmbyLibraryData(MediaServerEmby, []string{"keep"})
			case "stale":
				err = CleanupStaleEmbyMediaItemsByLibrarySyncRun(MediaServerEmby, "lib", "new")
			}
			if err != nil {
				t.Fatal(err)
			}
			var links []EmbyMediaSyncFile
			if err := conn.Find(&links).Error; err != nil || len(links) != 1 || links[0].MediaServerType != MediaServerJellyfin {
				t.Fatalf("cross-service cleanup: %+v %v", links, err)
			}
			count, err := GetEmbyMediaItemsCount(MediaServerJellyfin)
			if err != nil || count != 1 {
				t.Fatalf("other service lost media: %d %v", count, err)
			}
		})
	}
}
