package backup

import (
	"archive/zip"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestRestoreEmbyMediaLookupIndexes(t *testing.T) {
	for _, scenario := range []string{"rows", "empty", "idx_emby_server_part_lookup", "idx_emby_server_version_lookup"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			models.AllTables = []any{&models.EmbyMediaItem{}}
			if err := conn.AutoMigrate(&models.EmbyMediaItem{}); err != nil {
				t.Fatal(err)
			}
			if err := models.EnsureEmbyMediaLookupIndexes(conn); err != nil {
				t.Fatal(err)
			}
			before := models.EmbyMediaItem{BaseModel: models.BaseModel{ID: 9}, ItemId: "old", ServerId: "server", PartOfItemID: "old-parent", VersionOfItemID: "old-version"}
			if err := conn.Create(&before).Error; err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(helpers.ConfigDir, "backups"), 0755); err != nil {
				t.Fatal(err)
			}
			content := "{\"id\":1,\"item_id\":\"restored-1\",\"server_id\":\"server\",\"part_of_item_id\":\"parent\",\"version_of_item_id\":\"version\"}\n" +
				"{\"id\":2,\"item_id\":\"restored-2\",\"server_id\":\"server\",\"part_of_item_id\":\"parent\",\"version_of_item_id\":\"version\"}\n"
			if scenario == "empty" {
				content = ""
			}
			failIndex := strings.HasPrefix(scenario, "idx_")
			indexErr := errors.New("member lookup index creation failed")
			if failIndex {
				if err := conn.Callback().Raw().Before("gorm:raw").Register("test:member_lookup_index", func(tx *gorm.DB) {
					if strings.Contains(tx.Statement.SQL.String(), "CREATE INDEX IF NOT EXISTS "+scenario) {
						tx.AddError(indexErr)
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			err := Restore(writeBackupArchive(t, map[string]string{"EmbyMediaItem.json": content}, zip.Deflate))
			if failIndex {
				if !errors.Is(err, indexErr) || GetRunningResult().RestoreOutcome != "rolled_back" {
					t.Fatalf("index failure must roll back restore: %v, %+v", err, GetRunningResult())
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if conn.Migrator().HasTable(&models.SyncFile{}) {
				t.Fatal("restoring only media items must not require or create sync files")
			}
			for _, index := range []string{"idx_emby_server_part_lookup", "idx_emby_server_version_lookup"} {
				if !conn.Migrator().HasIndex(&models.EmbyMediaItem{}, index) {
					t.Fatalf("restore or rollback omitted member index %s", index)
				}
			}
			var rows []models.EmbyMediaItem
			if err := conn.Order("id").Find(&rows).Error; err != nil {
				t.Fatal(err)
			}
			switch {
			case failIndex:
				if len(rows) != 1 || rows[0] != before {
					t.Fatalf("failed restore changed original media: %+v", rows)
				}
			case scenario == "empty":
				if len(rows) != 0 {
					t.Fatalf("empty archive retained media: %+v", rows)
				}
			default:
				if len(rows) != 2 || rows[0].PartOfItemID != "parent" || rows[1].PartOfItemID != "parent" || rows[0].VersionOfItemID != "version" || rows[1].VersionOfItemID != "version" {
					t.Fatalf("restored member relationships changed: %+v", rows)
				}
			}
		})
	}
}
