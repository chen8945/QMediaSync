package backup

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"qmediasync/internal/models"
)

func TestBackupRestoreEmbyCleanupAuthorityAndAttempts(t *testing.T) {
	conn := setupBackupTest(t)
	models.AllTables = []any{
		&models.EmbyConfig{}, &models.Account{}, &models.SyncPath{}, &models.SyncFile{},
		&models.EmbyLibrarySyncPath{}, &models.EmbyIndexState{}, &models.EmbyItemState{},
		&models.EmbyMediaItem{}, &models.EmbyMediaSyncFile{}, &models.EmbyItemEvidence{},
		&models.EmbyWebhookRecord{}, &models.EmbyWebhookTarget{}, &models.EmbyObservedEvidenceIndex{}, &models.EmbyItemMembership{},
	}
	if err := conn.AutoMigrate(models.AllTables...); err != nil {
		t.Fatal(err)
	}
	config := models.EmbyConfig{EmbyUrl: "http://emby.invalid", EmbyApiKey: "backup-test-key", SyncEnabled: 1, EnableDeleteNetdisk: 1}
	account := models.Account{SourceType: models.SourceType115, UserId: "backup-test-user"}
	for _, value := range []any{&config, &account} {
		if err := conn.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	root := models.SyncPath{SourceType: account.SourceType, AccountId: account.ID, BaseCid: "root", LocalPath: "/strm", RemotePath: "/movies"}
	if err := conn.Create(&root).Error; err != nil {
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
	snapshot := models.EmbyItemSnapshot{Item: models.EmbyMediaItem{ItemId: "101", Type: "Movie", Path: file.LocalFilePath, LibraryId: "1"}, Sources: []models.EmbySnapshotSource{{ID: "source", ItemID: "101", PickCode: file.PickCode}}, MembersComplete: true}
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
	if err := conn.Model(&evidence).Update("sidecars_json", evidence.SidecarsJSON).Error; err != nil {
		t.Fatal(err)
	}
	_, err = models.SaveEmbyWebhook(t.Context(), models.EmbyWebhookEnvelope{Event: "library.deleted", ServerID: "server-a", ItemID: "101", ItemType: "Movie", ItemPath: file.LocalFilePath, Source: "official"})
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
	var recordsBefore []models.EmbyWebhookRecord
	var evidenceBefore []models.EmbyItemEvidence
	if err := conn.Order("id").Find(&recordsBefore).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Order("id").Find(&evidenceBefore).Error; err != nil {
		t.Fatal(err)
	}
	targetsBefore, err := models.LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := Backup(models.BackupTypeManual, "cleanup persistence"); err != nil {
		t.Fatal(err)
	}
	var archive models.BackupRecord
	if err := conn.Last(&archive).Error; err != nil || archive.FilePath == "" {
		t.Fatalf("archive: %+v %v", archive, err)
	}
	if err := conn.Model(&models.EmbyWebhookRecord{}).Where("id > 0").Update("plan_json", "corrupted after backup").Error; err != nil {
		t.Fatal(err)
	}
	if err := Restore(archive.FilePath); err != nil {
		t.Fatal(err)
	}
	var recordsAfter []models.EmbyWebhookRecord
	var evidenceAfter []models.EmbyItemEvidence
	if err := conn.Order("id").Find(&recordsAfter).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Order("id").Find(&evidenceAfter).Error; err != nil {
		t.Fatal(err)
	}
	targetsAfter, err := models.LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil || !reflect.DeepEqual(recordsBefore, recordsAfter) || !reflect.DeepEqual(evidenceBefore, evidenceAfter) || !reflect.DeepEqual(targetsBefore, targetsAfter) {
		t.Fatalf("backup restore changed authority, evidence, attempts or results: %v", err)
	}
	for _, row := range recordsAfter {
		var restored models.EmbyDeletionPlan
		if err := json.Unmarshal([]byte(row.PlanJSON), &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Input.CleanupPolicy != models.CurrentEmbyCleanupPolicy() {
			t.Fatalf("restored policy changed: record %d, policy=%+v", row.ID, restored.Input.CleanupPolicy)
		}
	}
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	reclaimed, err := models.ClaimEmbyWebhook(t.Context(), time.Now().Unix())
	if err != nil || reclaimed == nil || reclaimed.ID != record.ID || reclaimed.ClaimToken == record.ClaimToken {
		t.Fatalf("restored work did not receive a new claim: %+v %v", reclaimed, err)
	}
	if _, err := models.BeginEmbyWebhookAttempt(t.Context(), *reclaimed, plan.Targets[:1]); err == nil {
		t.Fatal("backup recovery replayed an already completed directory")
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
