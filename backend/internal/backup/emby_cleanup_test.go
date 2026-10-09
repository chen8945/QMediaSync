package backup

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestBackupRestoreEmbyCleanupAuthorityAndAttempts(t *testing.T) {
	allTables := models.AllTables
	setupBackupTest(t)
	models.AllTables = allTables
	conn, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "cleanup.sqlite")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := conn.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })
	db.Db = conn
	if existed, err := models.InitDB(); err != nil || existed {
		t.Fatalf("initialize current schema: existing=%t err=%v", existed, err)
	}
	fixture := seedEmbyCleanupBackup(t, conn)
	corruptEmbyCleanupBackupIndexes(t, conn)
	if err := Backup(models.BackupTypeManual, "cleanup persistence"); err != nil {
		t.Fatal(err)
	}
	var archive models.BackupRecord
	if err := conn.Last(&archive).Error; err != nil || archive.FilePath == "" {
		t.Fatalf("archive: %+v %v", archive, err)
	}
	for _, mutation := range []struct {
		model   any
		columns map[string]any
	}{
		{&models.EmbyWebhookRecord{}, map[string]any{"plan_json": "corrupted after backup"}},
		{&models.EmbyConfig{}, map[string]any{"path_mappings_json": "[]"}},
		{&models.SyncRootEvidence{}, map[string]any{"ancestors_json": "[]", "observed_at": 0, "invalidated_at": 0}},
		{&models.EmbyItemEvidence{}, map[string]any{"sources_json": "[]", "sidecars_json": "corrupted after backup"}},
	} {
		if err := conn.Model(mutation.model).Where("id > 0").Updates(mutation.columns).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := Restore(archive.FilePath); err != nil {
		t.Fatal(err)
	}
	assertEmbyCleanupBackup(t, conn, fixture)
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Unix())
	if err != nil || reclaimed == nil || reclaimed.ID != fixture.record.ID || reclaimed.ClaimToken == fixture.record.ClaimToken {
		t.Fatalf("restored work did not receive a new claim: %+v %v", reclaimed, err)
	}
	if _, err := models.BeginEmbyWebhookAttempt(t.Context(), *reclaimed, fixture.plan.Targets[:1]); err == nil {
		t.Fatal("backup recovery replayed an already completed directory")
	}
}

type embyCleanupBackupFixture struct {
	record       models.EmbyWebhookRecord
	plan         models.EmbyDeletionPlan
	config       models.EmbyConfig
	rootEvidence models.SyncRootEvidence
	records      []models.EmbyWebhookRecord
	evidence     []models.EmbyItemEvidence
	targets      []models.EmbyWebhookTarget
	members      []models.EmbyItemMembership
	observations []models.EmbyObservedEvidenceIndex
}

// 两种数据库共用已落库的合法证据，固定补全读取时间只用于检验持久化，不执行远端操作。
func seedEmbyCleanupBackup(t *testing.T, conn *gorm.DB) embyCleanupBackupFixture {
	t.Helper()
	account := models.Account{SourceType: models.SourceType115, UserId: "backup-test-user"}
	if err := conn.Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	root := models.SyncPath{SourceType: account.SourceType, AccountId: account.ID, BaseCid: "root", LocalPath: "/strm", RemotePath: "/movies"}
	if err := conn.Create(&root).Error; err != nil {
		t.Fatal(err)
	}
	mappings := []models.EmbyPathMapping{{EmbyRoot: "/emby/电影", LocalRoot: "/mounted/电影", SyncPathID: root.ID}}
	if _, err := models.NormalizeEmbyPathMappings(mappings); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&models.EmbyConfig{}).Where("id = ?", 1).Updates(map[string]any{
		"emby_url": "http://emby.invalid", "emby_api_key": "backup-test-key", "sync_enabled": 1,
		"enable_delete_netdisk": 1, "path_mappings_json": cleanupBackupJSON(t, mappings),
	}).Error; err != nil {
		t.Fatal(err)
	}
	var config models.EmbyConfig
	if err := conn.First(&config, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&models.Settings{}).Where("id = ?", 1).Update("strm_base_url", "https://qms.invalid").Error; err != nil {
		t.Fatal(err)
	}
	rootEvidence := models.SyncRootEvidence{
		BaseModel:  models.BaseModel{CreatedAt: 1700000001, UpdatedAt: 1700000002},
		SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account),
		RootFileID: root.BaseCid, RootPath: root.RemotePath, ObservedAt: 1700000003, InvalidatedAt: 1700000004,
		Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: root.RemotePath}},
	}
	if err := rootEvidence.Validate(); err != nil {
		t.Fatal(err)
	}
	rootEvidence.AncestorsJSON = cleanupBackupJSON(t, rootEvidence.Ancestors)
	if err := conn.Create(&rootEvidence).Error; err != nil {
		t.Fatal(err)
	}
	file := models.SyncFile{SourceType: account.SourceType, AccountId: account.ID, SyncPathId: root.ID, FileId: "video", ParentId: "feature-dir", FileName: "movie.mkv", Path: "/movies/Feature", LocalFilePath: root.GetFullLocalPath() + "/Feature/movie.strm", PickCode: "backup-pick", Sha1: "video-generation", FileSize: 100, MTime: 123, IsVideo: true}
	if err := conn.Create(&file).Error; err != nil {
		t.Fatal(err)
	}
	token, err := models.BeginEmbyIndexRead("server-a", &config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := models.EmbyItemSnapshot{
		Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", Path: "/emby/电影/Feature/movie.strm", LibraryId: "1"},
		Sources: []models.EmbySnapshotSource{
			{ID: "source", ItemID: "101", PickCode: file.PickCode, Path: "https://qms.invalid/115/newurl?pickcode=" + file.PickCode + "&userid=" + account.UserId},
			{ID: "external-source", ItemID: "101", PickCode: "external-pick", Path: "https://qms.invalid/115/newurl?pickcode=external-pick&userid=" + account.UserId},
		},
		MembersComplete:  true,
		MediaDirectories: []models.EmbyMediaDirectory{{ItemID: "101", MediaType: "Movie", LocalPath: "/emby/电影/Feature", EvidenceKind: "emby_physical_ancestor"}},
	}
	if err := models.ApplyEmbySnapshots(token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	var evidence models.EmbyItemEvidence
	if err := conn.First(&evidence).Error; err != nil {
		t.Fatal(err)
	}
	metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
	if err != nil {
		t.Fatal(err)
	}
	directory := models.EmbyFrozenFile{SyncPathID: root.ID, SourceType: root.SourceType, AccountID: account.ID, AccountIdentity: models.EmbyAccountIdentity(account), LocalRoot: root.GetFullLocalPath(), RemoteRoot: root.RemotePath, RootFileID: root.BaseCid, FileID: file.ParentId, ParentID: root.BaseCid, FileName: "Feature", Path: root.RemotePath, LocalFilePath: root.GetFullLocalPath() + "/Feature"}
	scope := models.EmbyDirectoryScope{Root: directory, MediaType: "Movie", ItemID: "101", LocalPath: directory.LocalFilePath, EvidenceKind: "qms_scrape_output", Ancestors: []models.EmbyDirectoryAncestor{{FileID: "0", Path: "/"}, {FileID: root.BaseCid, ParentID: "0", Path: root.RemotePath}}}
	metadata.DirectoryScopes = []models.EmbyDirectoryScope{scope}
	// 构造已保存的合法目录观察，后续走生产收件、计划、尝试和备份／恢复入口。
	evidence.SidecarsJSON = cleanupBackupJSON(t, metadata)
	if _, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON); err != nil {
		t.Fatal(err)
	}
	// 新增／修改观察使用真实保存入口，让恢复同时重建两个派生索引。
	if _, err := models.SaveEmbyWebhook(t.Context(), models.EmbyWebhookEnvelope{Event: "library.modified", ServerID: "server-a", ItemID: "101", ItemType: "Movie", ItemPath: snapshot.Item.Path, Source: "official"}); err != nil {
		t.Fatal(err)
	}
	observation, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Unix())
	if err != nil || observation == nil {
		t.Fatalf("claim observation: %+v %v", observation, err)
	}
	token, err = models.BeginEmbyIndexRead("server-a", &config)
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SaveEmbyObservedEvidence(t.Context(), *observation, token, []models.EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Model(&models.EmbyItemEvidence{}).Where("item_id = ?", snapshot.Item.ItemId).Update("sidecars_json", evidence.SidecarsJSON).Error; err != nil {
		t.Fatal(err)
	}
	if err := models.FinishEmbyWebhook(t.Context(), *observation, models.EmbyWebhookDone, "", 0, false); err != nil {
		t.Fatal(err)
	}
	_, err = models.SaveEmbyWebhook(t.Context(), models.EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: "101", ItemType: "Movie", ItemPath: snapshot.Item.Path, Source: "official"})
	if err != nil {
		t.Fatal(err)
	}
	record, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Unix())
	if err != nil || record == nil {
		t.Fatalf("claim: %+v %v", record, err)
	}
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 || len(input.DirectoryScopes) != 1 {
		t.Fatal("receipt did not freeze directory evidence")
	}
	owner := input.Owners[0]
	refs := []models.EmbyDeletionOwnerRef{{ItemID: owner.Item.ItemId, SnapshotID: owner.Evidence.ID, Generation: owner.Evidence.Generation}}
	video := models.EmbyDeletionTarget{Kind: "video", File: owner.Files[0], Owners: refs}
	video.Key = models.EmbyDeletionFileKey(video.File)
	operation := models.EmbyDeletionTarget{Key: "directory:" + models.EmbyDirectoryScopeKey(scope), Kind: "directory", File: directory, Directory: &scope, Owners: refs, CoveredKeys: []string{video.Key}}
	video.CoveredBy = operation.Key
	plan := models.EmbyDeletionPlan{Input: input, Targets: []models.EmbyDeletionTarget{operation, video}}
	var sources []models.EmbySnapshotSource
	if err := json.Unmarshal([]byte(owner.Evidence.SourcesJSON), &sources); err != nil || len(sources) != 2 {
		t.Fatalf("frozen sources: %+v %v", sources, err)
	}
	if sources[0].SourceType != account.SourceType || sources[0].AccountID != account.ID || sources[0].AccountIdentity != rootEvidence.AccountIdentity || sources[0].ObjectKind != "file_id" || sources[0].ObjectID != file.FileId || sources[0].SyncFileID != file.ID || sources[0].Reference != models.ResolveEmbySTRMReference(&config, snapshot.Item.Path) {
		t.Fatalf("bound source lost identity or explicit reference: %+v", sources[0])
	}
	external := models.EmbyFrozenFile{
		SourceType: account.SourceType, AccountID: account.ID, AccountIdentity: rootEvidence.AccountIdentity,
		FileID: "external-video", ParentID: "external-parent", FileName: "external.mkv", Path: "/external", PickCode: "external-pick",
		SHA1: "external-generation", FileSize: 200, MTime: 456, SourceID: sources[1].ID, Reference: sources[1].Reference, ExternalSource: true,
	}
	supplement := models.EmbyDeletionSupplement{Owner: refs[0], Source: sources[1], File: external, ReadAt: time.Date(2024, 1, 2, 3, 4, 5, 123456789, time.UTC)}
	if err := models.ValidateEmbyDeletionSupplement(t.Context(), owner, supplement); err != nil {
		t.Fatalf("invalid persisted supplement fixture: %v", err)
	}
	plan.Supplements = []models.EmbyDeletionSupplement{supplement}
	plan.Targets = append(plan.Targets, models.EmbyDeletionTarget{Key: models.EmbyDeletionFileKey(external), Kind: "video", File: external, Owners: refs, SourceBindings: plan.Supplements})
	if err := models.SaveEmbyWebhookPlan(t.Context(), *record, plan); err != nil {
		t.Fatal(err)
	}
	attempt, err := models.BeginEmbyWebhookAttempt(t.Context(), *record, plan.Targets[:1])
	if err != nil {
		t.Fatal(err)
	}
	if err := models.SaveEmbyWebhookBatchResults(t.Context(), *record, attempt, []models.EmbyDeletionResult{{Key: operation.Key, Outcome: models.EmbyDeletionDeleted, Reason: "confirmed_directory_operation"}}); err != nil {
		t.Fatal(err)
	}
	fixture := embyCleanupBackupFixture{record: *record, plan: plan}
	for _, value := range []any{&fixture.config, &fixture.rootEvidence} {
		if err := conn.First(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []any{&fixture.records, &fixture.evidence, &fixture.targets, &fixture.members, &fixture.observations} {
		if err := conn.Order("id").Find(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if len(fixture.members) != 1 || len(fixture.observations) != 1 {
		t.Fatalf("expected original derived rows: members=%d observations=%d", len(fixture.members), len(fixture.observations))
	}
	return fixture
}

func corruptEmbyCleanupBackupIndexes(t *testing.T, conn *gorm.DB) {
	t.Helper()
	for _, model := range []any{&models.EmbyItemMembership{}, &models.EmbyObservedEvidenceIndex{}} {
		if err := conn.Model(model).Where("id > 0").Update("item_id", "999").Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertEmbyCleanupBackup(t *testing.T, conn *gorm.DB, want embyCleanupBackupFixture) {
	t.Helper()
	var got embyCleanupBackupFixture
	for _, value := range []any{&got.config, &got.rootEvidence} {
		if err := conn.First(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, value := range []any{&got.records, &got.evidence, &got.targets, &got.members, &got.observations} {
		if err := conn.Order("id").Find(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	if !reflect.DeepEqual(want.config, got.config) || !reflect.DeepEqual(want.rootEvidence, got.rootEvidence) ||
		!reflect.DeepEqual(want.records, got.records) || !reflect.DeepEqual(want.evidence, got.evidence) || !reflect.DeepEqual(want.targets, got.targets) {
		t.Fatal("backup restore changed configuration, root evidence, immutable JSON, attempts or results")
	}
	loaded, err := models.LoadSyncRootEvidenceBatchTx(conn, []uint{want.rootEvidence.SyncPathID})
	if err != nil || len(loaded) != 1 || loaded[want.rootEvidence.SyncPathID].AncestorsJSON != want.rootEvidence.AncestorsJSON || loaded[want.rootEvidence.SyncPathID].InvalidatedAt != want.rootEvidence.InvalidatedAt {
		t.Fatalf("restored root evidence cannot be read or lost invalidation: %+v %v", loaded, err)
	}
	for _, row := range got.records {
		if row.PlanJSON == "" {
			continue
		}
		var restored models.EmbyDeletionPlan
		if err := json.Unmarshal([]byte(row.PlanJSON), &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Input.CleanupPolicy != models.CurrentEmbyCleanupPolicy() {
			t.Fatalf("restored policy changed: record %d, policy=%+v", row.ID, restored.Input.CleanupPolicy)
		}
		if cleanupBackupJSON(t, restored.Input) != row.InputJSON || !reflect.DeepEqual(restored.Supplements, want.plan.Supplements) || !reflect.DeepEqual(restored.Targets[len(restored.Targets)-1].SourceBindings, want.plan.Supplements) {
			t.Fatal("restored supplements changed their source, read time or immutable receipt input")
		}
	}
	for _, evidence := range got.evidence {
		metadata, err := models.DecodeEmbyMetadata(evidence.SidecarsJSON)
		if err != nil || metadata.Version != 2 || len(metadata.MediaDirectories) != 1 {
			t.Fatalf("restored media-directory evidence is invalid: %+v %v", metadata, err)
		}
	}
	// 派生行重建时会重新分配主键和时间，只比较从原始证据投影的全部业务列。
	for _, members := range [][]models.EmbyItemMembership{want.members, got.members} {
		for i := range members {
			members[i].BaseModel = models.BaseModel{}
		}
	}
	for _, observations := range [][]models.EmbyObservedEvidenceIndex{want.observations, got.observations} {
		for i := range observations {
			observations[i].BaseModel = models.BaseModel{}
		}
	}
	if !reflect.DeepEqual(want.members, got.members) || !reflect.DeepEqual(want.observations, got.observations) {
		t.Fatalf("derived indexes were not rebuilt from original evidence: members=%+v observations=%+v", got.members, got.observations)
	}
}

func cleanupBackupJSON(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}
