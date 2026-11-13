package models

import (
	"context"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestMediaLibraryMigrationSQLite(t *testing.T) {
	testMediaLibraryMigration(t, func(t *testing.T) (*gorm.DB, func() *gorm.DB) {
		reopen := setupFreshInitializationSQLite(t)
		return db.Db, reopen
	})
}

func testMediaLibraryMigration(t *testing.T, setup func(*testing.T) (*gorm.DB, func() *gorm.DB)) {
	t.Helper()
	t.Run("fresh", func(t *testing.T) {
		_, reopen := setup(t)
		if err := Migrate(); err != nil {
			t.Fatal(err)
		}
		assertMediaLibraryDefaults(t, reopen(), false)
	})
	for _, existing := range []bool{false, true} {
		for _, failVersion := range []bool{false, true} {
			t.Run(fmt.Sprintf("old_config_%t/rollback_%t", existing, failVersion), func(t *testing.T) {
				conn, reopen := setup(t)
				seedEmbyIdentityMigration(t, conn, 70)
				if err := conn.Migrator().DropIndex(&EmbyMediaItem{}, "idx_emby_item_id"); err != nil {
					t.Fatal(err)
				}
				if err := conn.Migrator().DropColumn(&EmbyMediaItem{}, "MediaServerType"); err != nil {
					t.Fatal(err)
				}
				if err := conn.Exec("CREATE UNIQUE INDEX idx_emby_item_id ON emby_media_items(item_id)").Error; err != nil {
					t.Fatal(err)
				}
				if existing {
					if err := conn.AutoMigrate(&EmbyConfig{}); err != nil {
						t.Fatal(err)
					}
					if err := conn.Model(&EmbyConfig{}).Create(map[string]any{
						"id": 1, "emby_url": "http://legacy.invalid", "emby_api_key": "legacy-key",
						"sync_enabled": 0, "enable_media_notification": 0, "enable_extract_media_info": 1,
						"selected_libraries": `["old-library"]`, "sync_all_libraries": 0,
					}).Error; err != nil {
						t.Fatal(err)
					}
					if err := conn.Migrator().DropColumn(&EmbyConfig{}, "Enabled"); err != nil {
						t.Fatal(err)
					}
				}
				if failVersion {
					remove := failEmbyMigrationVersionUpdate(t, conn)
					if err := Migrate(); err == nil {
						t.Fatal("injected migration failure succeeded")
					}
					assertEmbyMigrationVersion(t, conn, 70)
					if conn.Migrator().HasTable(&JellyfinConfig{}) || conn.Migrator().HasColumn(&EmbyConfig{}, "Enabled") || conn.Migrator().HasColumn(&EmbyMediaItem{}, "MediaServerType") {
						t.Fatal("failed migration retained new configuration schema")
					}
					remove()
					conn = reopen()
				}
				if err := Migrate(); err != nil {
					t.Fatal(err)
				}
				conn = reopen()
				assertMediaLibraryDefaults(t, conn, existing)
				if err := conn.Create(&EmbyMediaItem{MediaServerType: MediaServerJellyfin, ItemId: "00077", ItemIdInt: 77}).Error; err != nil {
					t.Fatalf("old single-item unique index survived: %v", err)
				}
				var oldItem EmbyMediaItem
				if err := conn.Where("media_server_type = ? AND item_id = ?", MediaServerEmby, "00077").First(&oldItem).Error; err != nil {
					t.Fatalf("legacy item did not inherit Emby: %v", err)
				}
				for _, field := range []struct {
					model  any
					column string
				}{{&EmbyLibraryRefreshTask{}, "MediaServerType"}, {&EmbyLibraryRefreshTask{}, "ServerID"}, {&DbDownloadTask{}, "MediaServerID"}} {
					if !conn.Migrator().HasColumn(field.model, field.column) {
						t.Fatalf("unified migration missed %T.%s", field.model, field.column)
					}
				}
				if existing {
					var config EmbyConfig
					if err := conn.First(&config).Error; err != nil || config.EmbyUrl != "http://legacy.invalid" ||
						config.EmbyApiKey != "legacy-key" || config.EnableExtractMediaInfo != 1 ||
						config.SelectedLibraries != `["old-library"]` || config.SyncAllLibraries != 0 {
						t.Fatalf("migration changed legacy values: %+v, %v", config, err)
					}
				}
				if err := Migrate(); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func assertMediaLibraryDefaults(t *testing.T, conn *gorm.DB, oldConfig bool) {
	t.Helper()
	assertEmbyMigrationVersion(t, conn, 71)
	var emby EmbyConfig
	var jellyfin JellyfinConfig
	if err := conn.First(&emby).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.First(&jellyfin).Error; err != nil {
		t.Fatal(err)
	}
	if emby.Enabled != oldConfig || emby.EnableMediaNotification != boolInt(oldConfig) || emby.SyncEnabled != 0 {
		t.Fatalf("Emby defaults changed: %+v", emby)
	}
	if jellyfin.ID != 1 || jellyfin.Enabled || jellyfin.SyncEnabled != 0 || jellyfin.EnableMediaNotification != 0 || jellyfin.EmbyUrl != "" || jellyfin.EmbyApiKey != "" {
		t.Fatalf("Jellyfin inherited configuration: %+v", jellyfin)
	}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func TestMediaLibraryEvidenceIsolationSQLite(t *testing.T) {
	setupFreshInitializationSQLite(t)
	testMediaLibraryEvidenceIsolation(t, db.Db)
}

func testMediaLibraryEvidenceIsolation(t *testing.T, conn *gorm.DB) {
	t.Helper()
	if err := Migrate(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	records := make(map[MediaServerType]EmbyWebhookRecord)
	tokens := make(map[MediaServerType]EmbyIndexToken)
	snapshot := EmbyItemSnapshot{
		Item:    EmbyMediaItem{ItemId: "101", Type: "Episode", Path: "/local/movie.mkv", SeasonId: "200", SeriesId: "300"},
		Sources: []EmbySnapshotSource{{ID: "source", ItemID: "101", Path: "/local/movie.mkv"}}, MembersComplete: true,
	}
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		updates := map[string]any{"enabled": true, "sync_enabled": 1, "enable_delete_netdisk": 1}
		if kind == MediaServerJellyfin {
			updates["jellyfin_url"], updates["jellyfin_api_key"] = "http://same.invalid", "same-key"
		} else {
			updates["emby_url"], updates["emby_api_key"] = "http://same.invalid", "same-key"
		}
		if err := UpdateMediaLibraryConfig(kind, updates); err != nil {
			t.Fatal(err)
		}
		config, err := ReadMediaLibraryConfigSnapshot(kind)
		if err != nil {
			t.Fatal(err)
		}
		token, err := BeginEmbyIndexRead("same-server", config)
		if err != nil {
			t.Fatal(err)
		}
		if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
		token, err = BeginEmbyIndexRead("same-server", config)
		if err != nil {
			t.Fatal(err)
		}
		tokens[kind] = token
		if _, err := SaveEmbyWebhook(ctx, EmbyWebhookEnvelope{MediaServerType: kind, Event: "library.modified",
			ServerID: token.ServerID, ItemID: "101", ItemType: "Episode", Source: "official"}); err != nil {
			t.Fatal(err)
		}
		record, err := ClaimEmbyWebhook(ctx, kind, 1<<62)
		if err != nil || record == nil {
			t.Fatalf("%s claim: %+v %v", kind, record, err)
		}
		if err := SaveEmbyObservedEvidence(ctx, *record, token, []EmbyItemSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
		records[kind] = *record
	}
	if err := RebuildEmbyItemMembership(conn); err != nil {
		t.Fatal(err)
	}
	if err := RebuildEmbyObservedEvidenceIndex(conn); err != nil {
		t.Fatal(err)
	}
	for kind, token := range tokens {
		evidence, err := loadEmbyScopeEvidenceTx(conn, kind, token.ServerID, token.ServerConfigKey, "200", "Season", nil)
		if err != nil || len(evidence) != 1 || evidence[0].MediaServerType != kind {
			t.Fatalf("%s membership crossed ownership: %+v %v", kind, evidence, err)
		}
		observed, err := loadEmbyObservedBatchTx(conn, records[kind])
		if err != nil || len(observed.evidence) != 1 {
			t.Fatalf("%s observation crossed ownership: %+v %v", kind, observed, err)
		}
		for _, entry := range observed.evidence {
			if entry.MediaServerType != kind {
				t.Fatalf("other service evidence included: %+v", entry)
			}
		}
	}
	target := EmbyDeletionTarget{Kind: "video", File: EmbyFrozenFile{SourceType: SourceType115, AccountID: 1, FileID: "same-file"}}
	target.Key = EmbyDeletionTargetKey(target)
	if err := conn.Create(&EmbyWebhookTarget{RecordID: records[MediaServerJellyfin].ID, ServerID: "same-server",
		ServerConfigKey: tokens[MediaServerJellyfin].ServerConfigKey, TargetKey: target.Key, Outcome: EmbyDeletionDeleted}).Error; err != nil {
		t.Fatal(err)
	}
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		record := records[kind]
		record.Authorized = true
		result, err := FindEmbyWebhookSuccess(ctx, record, target)
		if err != nil || (result != nil) != (kind == MediaServerJellyfin) {
			t.Fatalf("%s borrowed another service result: %+v %v", kind, result, err)
		}
	}

	// 同一物理账本有两端关系时，目录保留查询只使用本端的完整关系。
	file := SyncFile{SourceType: SourceType115, AccountId: 1, FileId: "same-file", Path: "/movies/Work", FileName: "movie.mkv"}
	if err := conn.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	directory := EmbyDirectoryScope{Root: EmbyFrozenFile{SourceType: SourceType115, AccountID: 1, Path: "/movies", FileName: "Work"}}
	states := make(map[MediaServerType]EmbyItemState)
	for _, kind := range []MediaServerType{MediaServerEmby, MediaServerJellyfin} {
		var state EmbyItemState
		if err := conn.Where("media_server_type = ? AND item_id = ?", kind, "101").First(&state).Error; err != nil {
			t.Fatal(err)
		}
		states[kind] = state
		if err := conn.Create(&EmbyMediaSyncFile{MediaServerType: kind, EmbyItemId: "101", SyncFileId: file.ID, SnapshotID: state.SnapshotID}).Error; err != nil {
			t.Fatal(err)
		}
		input := EmbyDeletionInput{MediaServerType: kind, ServerID: tokens[kind].ServerID, ServerConfigKey: tokens[kind].ServerConfigKey}
		target := EmbyDeletionTarget{Directory: &directory, Owners: []EmbyDeletionOwnerRef{{ItemID: "101", SnapshotID: state.SnapshotID, Generation: state.Generation}}}
		if err := embyCheckDirectoryRetainers(ctx, input, target); err != nil {
			t.Fatalf("%s directory borrowed another service relation: %v", kind, err)
		}
	}
	// 即使损坏的本端关系引用另一端一致的证据/state，也不能将其当作本端 owner。
	jellyfinState := states[MediaServerJellyfin]
	for _, model := range []any{&EmbyMediaSyncFile{}, &EmbyMediaItem{}} {
		if err := conn.Model(model).Where("media_server_type = ?", MediaServerEmby).UpdateColumn("snapshot_id", jellyfinState.SnapshotID).Error; err != nil {
			t.Fatal(err)
		}
	}
	input := EmbyDeletionInput{MediaServerType: MediaServerEmby, ServerID: tokens[MediaServerEmby].ServerID, ServerConfigKey: tokens[MediaServerEmby].ServerConfigKey}
	borrowedTarget := EmbyDeletionTarget{Directory: &directory, Owners: []EmbyDeletionOwnerRef{{ItemID: "101", SnapshotID: jellyfinState.SnapshotID, Generation: jellyfinState.Generation}}}
	if err := embyCheckDirectoryRetainers(ctx, input, borrowedTarget); err == nil {
		t.Fatal("directory query borrowed other service evidence through a damaged link")
	}
	if err := UpdateMediaLibraryConfig(MediaServerEmby, map[string]any{"enabled": false}); err != nil {
		t.Fatal(err)
	}
	emby := records[MediaServerEmby]
	if err := FinishEmbyWebhook(ctx, emby, EmbyWebhookRetry, "disabled", 0, false); err != nil {
		t.Fatal(err)
	}
	if claim, err := ClaimEmbyWebhook(ctx, MediaServerEmby, 1<<62); err != nil || claim != nil {
		t.Fatalf("disabled service claimed pending work: %+v %v", claim, err)
	}
	if err := conn.First(&emby, emby.ID).Error; err != nil || emby.Attempts != 0 || emby.Status != EmbyWebhookRetry {
		t.Fatalf("disabled work consumed retries: %+v %v", emby, err)
	}
}
