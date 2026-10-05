package models

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func setupEmbySnapshotModelTest(t *testing.T) (*EmbyConfig, EmbyIndexToken, SyncFile) {
	t.Helper()
	setupEmbyMediaTestDB(t)
	if err := MigrateEmbyDeletionSchema(db.Db); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.AutoMigrate(&EmbyConfig{}, &EmbyLibrarySyncPath{}, &SyncFile{}, &SyncPath{}, &Account{}); err != nil {
		t.Fatal(err)
	}
	config := &EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "test-key", SyncEnabled: 1}
	if err := db.Db.Create(config).Error; err != nil {
		t.Fatal(err)
	}
	account := Account{SourceType: SourceType115, UserId: "owner"}
	if err := db.Db.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := SyncPath{SourceType: SourceType115, AccountId: account.ID, LocalPath: "/strm", RemotePath: "/movies", BaseCid: "root"}
	if err := db.Db.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	file := SyncFile{SourceType: SourceType115, AccountId: account.ID, SyncPathId: root.ID, FileId: "f1", PickCode: "p1", Path: "/movies", LocalFilePath: root.GetFullLocalPath() + "/movie.strm", FileName: "movie.mkv", FileSize: 100, Sha1: "sha1", MTime: 123, IsVideo: true}
	if err := db.Db.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	token, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	return config, token, file
}

func snapshotForFile(file SyncFile) EmbyItemSnapshot {
	return EmbyItemSnapshot{Item: EmbyMediaItem{ItemId: "101", Type: "Movie", Path: file.LocalFilePath, LibraryId: "lib", PickCode: file.PickCode, MediaSourcePath: "http://qms/stream?pickcode=" + file.PickCode}, Sources: []EmbySnapshotSource{{ID: "source-101", ItemID: "101", Path: "http://qms/stream?pickcode=" + file.PickCode, PickCode: file.PickCode}}, LibraryName: "电影", MembersComplete: true}
}

func TestEmbySnapshotZeroValuesReplaceLinksAndPreserveHistory(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.SeasonId = "season"
	snapshot.Item.IndexNumber = 9
	snapshot.Item.IsFolder = true
	snapshot.Item.LastSeenSyncRun = "full-1"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var before EmbyMediaItem
	db.Db.First(&before)
	snapshot.Item.SeasonId = ""
	snapshot.Item.IndexNumber = 0
	snapshot.Item.IsFolder = false
	snapshot.Item.LastSeenSyncRun = ""
	snapshot.Item.PickCode = ""
	snapshot.Item.MediaSourcePath = "/local/movie.mkv"
	snapshot.Sources = []EmbySnapshotSource{{Path: "/local/movie.mkv"}}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var after EmbyMediaItem
	db.Db.First(&after)
	if after.SeasonId != "" || after.IndexNumber != 0 || after.IsFolder || after.PickCode != "" {
		t.Fatalf("zero values retained: %+v", after)
	}
	if after.CreatedAt != before.CreatedAt || after.LastSeenSyncRun != "full-1" {
		t.Fatal("local metadata cleared")
	}
	var count int64
	db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
	if count != 0 {
		t.Fatalf("stale links=%d", count)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence, before.SnapshotID).Error; err != nil {
		t.Fatal("old evidence erased", err)
	}
	var frozen []EmbyFrozenFile
	if err := json.Unmarshal([]byte(evidence.FilesJSON), &frozen); err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 1 || frozen[0].Reason != "" || frozen[0].FileID != "f1" {
		t.Fatalf("invalid history %+v", frozen)
	}
}

func TestEmbySnapshotWriteFailureRollsBackWholeGroup(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	initial := snapshotForFile(file)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{initial}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Callback().Create().Before("gorm:create").Register("test:fail_emby_link", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_media_sync_files" {
			tx.AddError(errors.New("injected link failure"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Create().Remove("test:fail_emby_link") })
	next := snapshotForFile(file)
	next.Item.Name = "must rollback"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{next}); err == nil {
		t.Fatal("expected failure")
	}
	var item EmbyMediaItem
	db.Db.First(&item)
	if item.Name != "" {
		t.Fatal("item partially updated")
	}
	var count int64
	db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
	if count != 1 {
		t.Fatal("old link lost")
	}
	db.Db.Model(&EmbyItemEvidence{}).Count(&count)
	if count != 1 {
		t.Fatal("failed snapshot history survived")
	}
}

func TestEmbySnapshotDeletionBarrierAndAdmission(t *testing.T) {
	config, old, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	if err := ApplyEmbySnapshots(old, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		var err error
		revision, err = RegisterEmbyDeletionTx(tx, "server-a", []string{"101", "202"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(old, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("old query admitted: %v", err)
	}
	fresh, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(fresh, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbyItemDeleted) {
		t.Fatalf("tombstone ignored: %v", err)
	}
	if err := CleanupEmbyLibrarySnapshot(old, "lib", "new-full"); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("stale cleanup admitted", err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error { return AdmitEmbyItemGenerationTx(tx, "server-a", "101", revision) }); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(old, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("old query after admission admitted", err)
	}
	newest, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(newest, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	snapshot.Item.ItemId = "202"
	snapshot.Sources = nil
	if err := ApplyEmbySnapshots(newest, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbyItemDeleted) {
		t.Fatal("unknown item barrier lost", err)
	}
	if err := CleanupEmbyLibrarySnapshot(newest, "lib", "new-full"); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Db.Model(&EmbyMediaItem{}).Count(&count)
	if count != 0 {
		t.Fatal("current index not cleaned")
	}
	db.Db.Model(&EmbyItemEvidence{}).Count(&count)
	if count == 0 {
		t.Fatal("cleanup erased history")
	}
}

func TestEmbySnapshotAmbiguousPickCodePreservesOldLinks(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	duplicate := file
	duplicate.BaseModel = BaseModel{}
	duplicate.AccountId = 999
	duplicate.FileId = "other"
	if err := db.Db.Create(&duplicate).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); !errors.Is(err, ErrEmbyIdentityAmbiguous) {
		t.Fatal("ambiguous matched", err)
	}
	var count int64
	db.Db.Model(&EmbyMediaSyncFile{}).Count(&count)
	if count != 1 {
		t.Fatal("old link removed")
	}
}

func TestEmbySnapshotMembershipSurvivesVideoQueryThenDetaches(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	parent := snapshotForFile(file)
	parent.Item.PartCount = 2
	child := snapshotForFile(file)
	child.Item.ItemId = "102"
	child.Item.Type = "Video"
	child.Item.PartOfItemID = "101"
	child.Sources[0].ItemID = "102"
	child.MembersComplete = false
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent, child}); err != nil {
		t.Fatal(err)
	}
	child.Item.PartOfItemID = ""
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{child}); err != nil {
		t.Fatal(err)
	}
	var item EmbyMediaItem
	db.Db.Where("item_id = ?", "102").First(&item)
	if item.PartOfItemID != "101" {
		t.Fatal("independent Video query lost parent")
	}
	parent.Item.PartCount = 1
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent}); err != nil {
		t.Fatal(err)
	}
	db.Db.Where("item_id = ?", "102").First(&item)
	if item.PartOfItemID != "" {
		t.Fatal("complete parent did not detach obsolete child")
	}
}

func TestEmbySnapshotEvidenceRedactsReplayCredentials(t *testing.T) {
	_, token, file := setupEmbySnapshotModelTest(t)
	snapshot := snapshotForFile(file)
	secretURL := "https://user:password@qms/stream?pickcode=p1&token=secret-token&sign=secret-sign#secret-fragment"
	snapshot.Item.MediaSourcePath = secretURL
	snapshot.Sources[0].Path = secretURL
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var evidence EmbyItemEvidence
	db.Db.First(&evidence)
	for _, secret := range []string{"password", "secret-token", "secret-sign", "secret-fragment"} {
		if strings.Contains(evidence.ItemJSON+evidence.SourcesJSON+evidence.FilesJSON, secret) {
			t.Fatal("credential saved in historical evidence")
		}
	}
}

func TestEmbySnapshotFinishChecksTokenEvenWithoutItems(t *testing.T) {
	config, token, _ := setupEmbySnapshotModelTest(t)
	if err := db.Db.Model(config).Updates(map[string]any{"last_saved_cursor_at": 100, "last_full_sync_at": 50}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error { _, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101"}); return err }); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyIndexSyncRun(token, EmbySyncModeIncremental, 0, 200, 150, nil); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("zero-item stale run advanced", err)
	}
	var fresh EmbyConfig
	db.Db.First(&fresh)
	if fresh.LastSavedCursorAt != 100 || fresh.LastFullSyncAt != 50 || fresh.LastError == "" {
		t.Fatalf("bad finished state %+v", fresh)
	}
}

func TestEmbySnapshotDetachKeepsOldFileGeneration(t *testing.T) {
	for _, relation := range []string{"part", "version"} {
		t.Run(relation, func(t *testing.T) {
			testEmbySnapshotDetachKeepsOldFileGeneration(t, relation)
		})
	}
}

func testEmbySnapshotDetachKeepsOldFileGeneration(t *testing.T, relation string) {
	t.Helper()
	_, token, file := setupEmbySnapshotModelTest(t)
	parent := snapshotForFile(file)
	parent.Item.PartCount = 2
	child := snapshotForFile(file)
	child.Item.ItemId = "102"
	child.Item.Type = "Video"
	if relation == "part" {
		child.Item.PartOfItemID = "101"
	} else {
		child.Item.VersionOfItemID = "101"
	}
	child.Sources[0].ItemID = "102"
	child.MembersComplete = false
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent, child}); err != nil {
		t.Fatal(err)
	}
	var beforeState EmbyItemState
	if err := db.Db.Where("item_id = ?", "102").First(&beforeState).Error; err != nil {
		t.Fatal(err)
	}
	var beforeEvidence EmbyItemEvidence
	if err := db.Db.First(&beforeEvidence, beforeState.SnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Model(&file).Updates(map[string]any{"file_id": "new-file", "sha1": "new-sha"}).Error; err != nil {
		t.Fatal(err)
	}
	parent.Item.PartCount = 1
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent}); err != nil {
		t.Fatal(err)
	}
	var current EmbyMediaItem
	if err := db.Db.Where("item_id = ?", "102").First(&current).Error; err != nil {
		t.Fatal(err)
	}
	if current.PartOfItemID != "" || current.VersionOfItemID != "" || current.Generation != beforeState.Generation || current.SnapshotID == beforeState.SnapshotID {
		t.Fatalf("membership changed physical generation: %+v", current)
	}
	var evidence EmbyItemEvidence
	if err := db.Db.First(&evidence, current.SnapshotID).Error; err != nil {
		t.Fatal(err)
	}
	if evidence.FilesJSON != beforeEvidence.FilesJSON || evidence.SourcesJSON != beforeEvidence.SourcesJSON || evidence.SidecarsJSON != beforeEvidence.SidecarsJSON || evidence.Generation != beforeEvidence.Generation || evidence.CreatedAt != beforeEvidence.CreatedAt {
		t.Fatal("detaching replaced the original observation")
	}
	var original EmbyItemEvidence
	if err := db.Db.First(&original, beforeEvidence.ID).Error; err != nil || original != beforeEvidence {
		t.Fatalf("detaching rewrote immutable evidence: %v", err)
	}
	var frozen []EmbyFrozenFile
	if err := json.Unmarshal([]byte(evidence.FilesJSON), &frozen); err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 1 || frozen[0].FileID != "f1" || frozen[0].SHA1 != "sha1" {
		t.Fatalf("detaching reobserved current file: %+v", frozen)
	}
}

func TestEmbySnapshotDetachRejectsUnsupportedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
	}{
		{name: "legacy_empty_array", raw: `[]`},
		{name: "legacy_file_array", raw: `[{"file_id":"sidecar-1"}]`},
		{name: "malformed", raw: `{`},
		{name: "unsupported_version", raw: `{"version":1,"exclusive_files":[],"scoped_files":[],"directory_scopes":[]}`},
	} {
		for _, relation := range []string{"part", "version"} {
			t.Run(tc.name+"/"+relation, func(t *testing.T) {
				_, token, file := setupEmbySnapshotModelTest(t)
				parent := snapshotForFile(file)
				parent.Item.PartCount = 2
				child := snapshotForFile(file)
				child.Item.ItemId, child.Item.Type = "102", "Video"
				child.Sources[0].ItemID = "102"
				child.MembersComplete = false
				if relation == "part" {
					child.Item.PartOfItemID = "101"
				} else {
					child.Item.VersionOfItemID = "101"
				}
				if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent, child}); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Model(&EmbyItemEvidence{}).Where("item_id = ?", "102").Update("sidecars_json", tc.raw).Error; err != nil {
					t.Fatal(err)
				}
				type snapshotState struct {
					Items    []EmbyMediaItem
					States   []EmbyItemState
					Evidence []EmbyItemEvidence
					Links    []EmbyMediaSyncFile
				}
				load := func() snapshotState {
					t.Helper()
					var saved snapshotState
					for _, rows := range []any{&saved.Items, &saved.States, &saved.Evidence, &saved.Links} {
						if err := db.Db.Order("id").Find(rows).Error; err != nil {
							t.Fatal(err)
						}
					}
					return saved
				}
				before := load()
				parent.Item.PartCount = 1
				if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{parent}); err == nil {
					t.Fatal("detaching copied unsupported metadata into a new snapshot")
				}
				if after := load(); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected membership change did not roll back index, links, state and immutable evidence")
				}
			})
		}
	}
}

func TestEmbySnapshotFinishRedactsTransportCredentials(t *testing.T) {
	_, token, _ := setupEmbySnapshotModelTest(t)
	failure := errors.New(`Get "https://user:password@emby.invalid/emby/Items?api_key=secret-key&token=secret-token": dial refused`)
	if err := FinishEmbyIndexSyncRun(token, EmbySyncModeFull, 0, 200, 0, failure); err != nil {
		t.Fatal(err)
	}
	var config EmbyConfig
	db.Db.First(&config)
	for _, secret := range []string{"password", "secret-key", "secret-token"} {
		if strings.Contains(config.LastError, secret) {
			t.Fatal("persisted replay credential")
		}
	}
}

func TestEmbySnapshotLibrarySelectionDoesNotChangeServerEvidenceIdentity(t *testing.T) {
	config, before, file := setupEmbySnapshotModelTest(t)
	if err := ApplyEmbySnapshots(before, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	config.SyncAllLibraries = 0
	config.SelectedLibraries = `["other-library"]`
	if err := db.Db.Model(config).Select("sync_all_libraries", "selected_libraries").Updates(config).Error; err != nil {
		t.Fatal(err)
	}
	after, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if before.ConfigKey == after.ConfigKey || before.ServerConfigKey != after.ServerConfigKey {
		t.Fatal("sync selection became deletion authorization")
	}
	var evidence EmbyItemEvidence
	db.Db.First(&evidence)
	if evidence.ServerConfigKey != after.ServerConfigKey {
		t.Fatal("old evidence lost same-server identity")
	}
}
