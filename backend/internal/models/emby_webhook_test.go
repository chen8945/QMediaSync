package models

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func setupEmbyWebhookModelTest(t *testing.T) (*EmbyConfig, EmbyIndexToken, SyncFile) {
	t.Helper()
	config, token, file := setupEmbySnapshotModelTest(t)
	if err := db.Db.Model(config).Update("enable_delete_netdisk", 1).Error; err != nil {
		t.Fatal(err)
	}
	return config, token, file
}

func webhookModelEnvelope(event string, file SyncFile) EmbyWebhookEnvelope {
	return EmbyWebhookEnvelope{Event: event, ServerID: "server-a", ItemID: "101", ItemType: "Movie", ItemPath: file.LocalFilePath, Source: "official"}
}
func webhookModelClaim(t *testing.T) EmbyWebhookRecord {
	t.Helper()
	record, err := ClaimEmbyWebhook(t.Context(), time.Now().Unix()+10)
	if err != nil || record == nil {
		t.Fatalf("claim: %+v %v", record, err)
	}
	return *record
}
func webhookModelInput(t *testing.T, record EmbyWebhookRecord) EmbyDeletionInput {
	t.Helper()
	var input EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	return input
}
func webhookModelPlan(t *testing.T, record EmbyWebhookRecord) EmbyDeletionPlan {
	t.Helper()
	input := webhookModelInput(t, record)
	provider := &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{}}
	for _, owner := range input.Owners {
		for _, file := range owner.Files {
			provider.files[file.FileID] = embyRemoteFromFrozen(file)
		}
	}
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
	if err != nil {
		t.Fatal(err)
	}
	return plan
}
func assertWebhookCount(t *testing.T, model any, want int64) {
	t.Helper()
	var count int64
	if err := db.Db.Model(model).Count(&count).Error; err != nil || count != want {
		t.Fatalf("count %T=%d want%d err%v", model, count, want, err)
	}
}

func TestEmbyWebhookReceiptRollbackAndIndependentEvents(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("receipt fail")
	if err := db.Db.Callback().Create().Before("gorm:create").Register("test:webhook_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_records" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	db.Db.Callback().Create().Remove("test:webhook_fail")
	var state EmbyItemState
	db.Db.First(&state)
	if state.Deleted {
		t.Fatal("failed receipt left barrier")
	}
	assertWebhookCount(t, &EmbyWebhookRecord{}, 0)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
			t.Fatal(err)
		}
	}
	assertWebhookCount(t, &EmbyWebhookRecord{}, 2)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("old read accepted", err)
	}
}

func TestEmbyWebhookEarlyObservationAdoptWithoutIndex(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.new", file)); err != nil {
		t.Fatal(err)
	}
	newRecord := webhookModelClaim(t)
	if err := SaveEmbyObservedEvidence(t.Context(), newRecord, token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyItemEvidence{}, 1)
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	assertWebhookCount(t, &EmbyMediaSyncFile{}, 0)
	assertWebhookCount(t, &EmbyItemState{}, 0)
	deleted, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
	if err != nil {
		t.Fatal(err)
	}
	input := webhookModelInput(t, deleted)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 || input.Owners[0].Files[0].FileID != file.FileId {
		t.Fatalf("observation not adopted: %+v", input)
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	assertWebhookCount(t, &EmbyMediaSyncFile{}, 0)
	if err := SaveEmbyObservedEvidence(t.Context(), newRecord, token, []EmbyItemSnapshot{snapshotForFile(file)}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("late observation accepted", err)
	}
}

func TestEmbyWebhookObservationCannotReplaceNewSnapshot(t *testing.T) {
	config, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	var err error
	token, err = BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.modified", file)); err != nil {
		t.Fatal(err)
	}
	observation := webhookModelClaim(t)
	old := snapshotForFile(file)
	old.Item.Name = "old observed"
	if err := SaveEmbyObservedEvidence(t.Context(), observation, token, []EmbyItemSnapshot{old}); err != nil {
		t.Fatal(err)
	}
	current := snapshotForFile(file)
	current.Item.Name = "new snapshot"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{current}); err != nil {
		t.Fatal(err)
	}
	record, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
	if err != nil {
		t.Fatal(err)
	}
	if got := webhookModelInput(t, record).Owners[0].Item.Name; got != "new snapshot" {
		t.Fatal("adopted stale observation", got)
	}
}

func TestEmbyWebhookClaimRetryRecoveryAndImmutablePlan(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	first := webhookModelClaim(t)
	plan := webhookModelPlan(t, first)
	if err := SaveEmbyWebhookResult(t.Context(), first, EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionDeleted}); err == nil {
		t.Fatal("result without plan accepted")
	}
	if err := SaveEmbyWebhookPlan(t.Context(), first, plan); err != nil {
		t.Fatal(err)
	}
	forged := plan
	forged.Input.Authorized = false
	if err := SaveEmbyWebhookPlan(t.Context(), first, forged); err == nil {
		t.Fatal("immutable input replaced")
	}
	result := EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed, Reason: "request outcome unknown"}
	if err := SaveEmbyWebhookResult(t.Context(), first, result); err != nil {
		t.Fatal(err)
	}
	if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := webhookModelClaim(t)
	if recovered.ClaimToken == first.ClaimToken || recovered.PlanJSON == "" {
		t.Fatal("recovery lost plan or retained old claim")
	}
	if err := FinishEmbyWebhook(t.Context(), first, EmbyWebhookDone, "", 0, false); !errors.Is(err, ErrEmbyWebhookClaimLost) {
		t.Fatal("stale claimant won", err)
	}
	targets, err := LoadEmbyWebhookTargets(t.Context(), recovered.ID)
	if err != nil || len(targets) != 1 || targets[0].Attempts != 1 || targets[0].Outcome != EmbyDeletionFailed {
		t.Fatalf("results lost %+v %v", targets, err)
	}
	result.Outcome = EmbyDeletionDeleted
	if err := SaveEmbyWebhookResult(t.Context(), recovered, result); err != nil {
		t.Fatal(err)
	}
	result.Outcome = EmbyDeletionFailed
	if err := SaveEmbyWebhookResult(t.Context(), recovered, result); err != nil {
		t.Fatal(err)
	}
	targets, _ = LoadEmbyWebhookTargets(t.Context(), recovered.ID)
	if targets[0].Outcome != EmbyDeletionDeleted || targets[0].Attempts != 1 {
		t.Fatal("success overwritten")
	}
	if err := FinishEmbyWebhook(t.Context(), recovered, EmbyWebhookRetry, "busy", 0, false); err != nil {
		t.Fatal(err)
	}
	next := webhookModelClaim(t)
	if next.Attempts != 0 {
		t.Fatal("busy consumed attempts")
	}
	reused, err := FindEmbyWebhookSuccess(t.Context(), next, plan.Targets[0])
	if err != nil || reused == nil {
		t.Fatal("success not reused", err)
	}
	changed := plan.Targets[0]
	changed.File.SHA1 = "replacement"
	changed.Key = EmbyDeletionFileKey(changed.File)
	if result, err := FindEmbyWebhookSuccess(t.Context(), next, changed); err != nil || result != nil {
		t.Fatal("new physical generation reused", err)
	}
}

func TestEmbyWebhookAuthorizationSnapshotAndSurvival(t *testing.T) {
	config, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	db.Db.Model(config).Update("enable_delete_netdisk", 0)
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	db.Db.Model(config).Update("enable_delete_netdisk", 1)
	if record.Authorized || webhookModelInput(t, record).Authorized {
		t.Fatal("receipt authorization elevated")
	}
	if err := AdmitEmbyWebhookSurvivors(t.Context(), record, []string{"101"}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatal("pre-admission read accepted", err)
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 1)
	if err := FinalizeEmbyWebhookLocal(t.Context(), record, []string{"101"}); err != nil {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 1)
}

func TestEmbyWebhookObservedNewPriorityAndRunningFollowup(t *testing.T) {
	_, _, file := setupEmbyWebhookModelTest(t)
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.new", file)); err != nil {
		t.Fatal(err)
	}
	first := webhookModelClaim(t)
	if first.Event != "library.new" {
		t.Fatal("early observation delayed")
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.new", file)); err != nil {
		t.Fatal(err)
	}
	second := webhookModelClaim(t)
	if second.ID == first.ID || second.Event != "library.new" {
		t.Fatal("new event during running lost")
	}
	if err := MarkEmbyWebhookObserved(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), first, EmbyWebhookRetry, "busy", 0, false); err != nil {
		t.Fatal(err)
	}
	next := webhookModelClaim(t)
	if next.Event != "library.deleted" {
		t.Fatal("observed busy sync starved deletion")
	}
}

func TestEmbyWebhookSidecarsUseOnlyPredatingEvidence(t *testing.T) {
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "historical", true: "replaced before receipt"}[replace], func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			meta := file
			meta.BaseModel = BaseModel{}
			meta.FileId = "meta-old"
			meta.FileName = "movie.nfo"
			meta.LocalFilePath = strings.TrimSuffix(file.LocalFilePath, ".strm") + ".nfo"
			meta.IsVideo = false
			meta.IsMeta = true
			meta.PickCode = "meta"
			if err := db.Db.Create(&meta).Error; err != nil {
				t.Fatal(err)
			}
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if replace {
				if err := db.Db.Model(&meta).Updates(map[string]any{"file_id": "meta-new", "sha1": "new-sha"}).Error; err != nil {
					t.Fatal(err)
				}
			}
			record, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, record)
			if len(input.Sidecars) != 1 || input.Sidecars[0].FileID != "meta-old" {
				t.Fatal("receipt manufactured history", input.Sidecars)
			}
			remote := embyRemoteFromFrozen(input.Sidecars[0])
			if replace {
				remote.FileID = "meta-new"
				remote.SHA1 = "new-sha"
			}
			video := input.Owners[0].Files[0]
			provider := &embyDeleteTestProvider{files: map[string]EmbyRemoteFile{video.FileID: embyRemoteFromFrozen(video), remote.FileID: remote}}
			plan, err := BuildEmbyDeletionPlan(t.Context(), input, func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil })
			if err != nil {
				t.Fatal(err)
			}
			want := 2
			if replace {
				want = 1
			}
			if len(plan.Targets) != want {
				t.Fatalf("targets=%d want%d", len(plan.Targets), want)
			}
			if replace && !slices.ContainsFunc(plan.Issues, func(issue string) bool { return strings.HasPrefix(issue, "sidecar_identity_changed:") }) {
				t.Fatal("replaced sidecar lost reason", plan.Issues)
			}
		})
	}
}

func TestEmbyWebhookRejectsMalformedSidecarEvidence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sidecars string
	}{
		{name: "empty"},
		{name: "whitespace", sidecars: " \n\t"},
		{name: "invalid_json", sidecars: "{"},
		{name: "object_instead_of_array", sidecars: `{"file_id":"not-an-array"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			var before EmbyItemState
			if err := db.Db.First(&before).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Db.Model(&EmbyItemEvidence{}).Where("id > 0").Update("sidecars_json", tc.sidecars).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err == nil {
				t.Fatal("malformed sidecar evidence authorized deletion")
			}
			assertWebhookCount(t, &EmbyWebhookRecord{}, 0)
			assertWebhookCount(t, &EmbyWebhookTarget{}, 0)
			assertWebhookCount(t, &EmbyMediaItem{}, 1)
			assertWebhookCount(t, &EmbyMediaSyncFile{}, 1)
			var after EmbyItemState
			if err := db.Db.First(&after, before.ID).Error; err != nil || after != before {
				t.Fatalf("rejected receipt changed item state: before=%+v after=%+v err=%v", before, after, err)
			}
		})
	}
}

func TestEmbyWebhookPlanFailureAndBackupLimitRollback(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	envelope := webhookModelEnvelope("library.deleted", file)
	envelope.DeepMountPaths = strings.Repeat("x", EmbyWebhookMaxStoredBytes)
	if _, err := SaveEmbyWebhook(t.Context(), envelope); err == nil {
		t.Fatal("oversized receipt saved")
	}
	assertWebhookCount(t, &EmbyWebhookRecord{}, 0)
	var state EmbyItemState
	db.Db.First(&state)
	if state.Deleted {
		t.Fatal("oversize left barrier")
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	plan := webhookModelPlan(t, record)
	injected := errors.New("target fail")
	db.Db.Callback().Create().Before("gorm:create").Register("test:target_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_targets" {
			tx.AddError(injected)
		}
	})
	t.Cleanup(func() { db.Db.Callback().Create().Remove("test:target_fail") })
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyWebhookTarget{}, 0)
	var saved EmbyWebhookRecord
	db.Db.First(&saved, record.ID)
	if saved.PlanJSON != "" {
		t.Fatal("partial plan persisted")
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := FinishEmbyWebhook(cancelled, record, EmbyWebhookDone, "", 0, false); err == nil {
		t.Fatal("cancelled storage succeeded")
	}
}

func TestEmbyWebhookDeepCandidatesNeverExpandConfirmedScope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidate  EmbyWebhookCandidate
		wantStatus string
		confirmed  bool
		issue      string
	}{
		{name: "known pickcode", candidate: EmbyWebhookCandidate{PickCode: "p1"}, wantStatus: EmbyWebhookPending, confirmed: true},
		{name: "unknown part", candidate: EmbyWebhookCandidate{Path: "/unverified/other.mkv"}, wantStatus: EmbyWebhookPending, issue: "candidate_unresolved"},
		{name: "known path wrong identity", candidate: EmbyWebhookCandidate{PickCode: "other"}, wantStatus: EmbyWebhookUnresolved, issue: "candidate_identity_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if tc.name == "known path wrong identity" {
				tc.candidate.Path = file.LocalFilePath
			}
			envelope := webhookModelEnvelope("deep.delete", file)
			envelope.Candidates = []EmbyWebhookCandidate{tc.candidate}
			record, err := SaveEmbyWebhook(t.Context(), envelope)
			if err != nil {
				t.Fatal(err)
			}
			input := webhookModelInput(t, record)
			if record.Status != tc.wantStatus || len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 {
				t.Fatalf("scope/status changed: %+v %+v", record, input)
			}
			if tc.confirmed && len(input.CandidateKeys) != 1 {
				t.Fatal("independent known identity not recognized")
			}
			if tc.issue != "" && !slices.Contains(input.Issues, tc.issue) {
				t.Fatal("missing diagnostic", input.Issues)
			}
		})
	}
}

func TestEmbyWebhookPayloadAndResultRedactCredentials(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	envelope := webhookModelEnvelope("deep.delete", file)
	secret := "https://user:password@example.test/get?pickcode=p1&token=secret-token#fragment"
	envelope.Candidates = []EmbyWebhookCandidate{{Path: secret, PickCode: "p1"}}
	envelope.DeepMountPaths = secret
	record, err := SaveEmbyWebhook(t.Context(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"password", "secret-token", "fragment"} {
		if strings.Contains(record.PayloadJSON, value) {
			t.Fatal("credential persisted", value)
		}
	}
	record = webhookModelClaim(t)
	plan := webhookModelPlan(t, record)
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyWebhookResult(t.Context(), record, EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed, Reason: "GET " + secret}); err != nil {
		t.Fatal(err)
	}
	targets, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(targets[0].Reason, "password") || strings.Contains(targets[0].Reason, "secret-token") {
		t.Fatal("error leaked credentials")
	}
}

func TestEmbyWebhookSuccessDoesNotReuseBaiduReplacement(t *testing.T) {
	_, _, _ = setupEmbyWebhookModelTest(t)
	file := EmbyFrozenFile{SourceType: SourceTypeBaiduPan, AccountID: 7, AccountIdentity: "baidu-user", FileID: "/movies/movie.mkv", Path: "/movies", FileName: "movie.mkv", PickCode: "old-fs-id", FileSize: 1024, MTime: 1234}
	record := EmbyWebhookRecord{ServerID: "server-a", ServerConfigKey: "connection", Authorized: true}
	prior := EmbyWebhookTarget{RecordID: 1, ServerID: record.ServerID, ServerConfigKey: record.ServerConfigKey, TargetKey: EmbyDeletionFileKey(file), Outcome: EmbyDeletionDeleted}
	if err := db.Db.Create(&prior).Error; err != nil {
		t.Fatal(err)
	}
	file.PickCode = "new-fs-id"
	target := EmbyDeletionTarget{Key: EmbyDeletionFileKey(file), File: file}
	if result, err := FindEmbyWebhookSuccess(t.Context(), record, target); err != nil || result != nil {
		t.Fatalf("replacement reused old fs_id success: %+v, %v", result, err)
	}
}

func TestEmbyWebhookIndependentObservationsSurviveOtherItemDeletion(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	for _, id := range []string{"101", "102"} {
		envelope := webhookModelEnvelope("library.new", file)
		envelope.ItemID = id
		if _, err := SaveEmbyWebhook(t.Context(), envelope); err != nil {
			t.Fatal(err)
		}
		snapshot := snapshotForFile(file)
		snapshot.Item.ItemId = id
		for i := range snapshot.Sources {
			snapshot.Sources[i].ItemID = id
		}
		if err := SaveEmbyObservedEvidence(t.Context(), webhookModelClaim(t), token, []EmbyItemSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"101", "102"} {
		envelope := webhookModelEnvelope("library.deleted", file)
		envelope.ItemID = id
		record, err := SaveEmbyWebhook(t.Context(), envelope)
		if err != nil {
			t.Fatal(err)
		}
		input := webhookModelInput(t, record)
		if len(input.Owners) != 1 || input.Owners[0].Item.ItemId != id {
			t.Fatalf("unrelated deletion discarded %s observation: %+v", id, input)
		}
	}
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	assertWebhookCount(t, &EmbyMediaSyncFile{}, 0)
}

func TestEmbyWebhookObservationCannotCrossSameItemBarrierAdmission(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.new", file)); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyObservedEvidence(t.Context(), webhookModelClaim(t), token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		revision, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101"})
		if err != nil {
			return err
		}
		return AdmitEmbyItemGenerationTx(tx, "server-a", "101", revision)
	}); err != nil {
		t.Fatal(err)
	}
	record, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
	if err != nil {
		t.Fatal(err)
	}
	if input := webhookModelInput(t, record); len(input.Owners) != 0 {
		t.Fatalf("old observation crossed same-item lifecycle barrier: %+v", input)
	}
}

func TestEmbyWebhookObservationCannotCrossExistingStateBarrierAdmission(t *testing.T) {
	config, token, file := setupEmbyWebhookModelTest(t)
	snapshot := snapshotForFile(file)
	snapshot.Item.Name = "current snapshot"
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	beforeGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.modified", file)); err != nil {
		t.Fatal(err)
	}
	snapshot.Item.Name = "observation before lifecycle barrier"
	if err := SaveEmbyObservedEvidence(t.Context(), webhookModelClaim(t), beforeGET, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Transaction(func(tx *gorm.DB) error {
		revision, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101"})
		if err != nil {
			return err
		}
		return AdmitEmbyItemGenerationTx(tx, "server-a", "101", revision)
	}); err != nil {
		t.Fatal(err)
	}
	record, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
	if err != nil {
		t.Fatal(err)
	}
	input := webhookModelInput(t, record)
	if len(input.Owners) != 1 || input.Owners[0].Item.Name != "current snapshot" {
		t.Fatalf("observation crossed barrier despite unchanged row/snapshot/generation: %+v", input)
	}
}

func TestEmbyWebhookLocalFinalizationIsExplicitAndConditional(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*testing.T, EmbyWebhookRecord)
		ids       []string
		wantError bool
		wantItems int64
	}{
		{name: "authorized protected old item", ids: []string{"101"}, wantItems: 0},
		{name: "no implicit scope", wantItems: 1},
		{name: "unknown ID", ids: []string{"102"}, wantError: true, wantItems: 1},
		{name: "later barrier", ids: []string{"101"}, wantItems: 1, mutate: func(t *testing.T, _ EmbyWebhookRecord) {
			if err := db.Db.Transaction(func(tx *gorm.DB) error { _, err := RegisterEmbyDeletionTx(tx, "server-a", []string{"101"}); return err }); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "restored evidence collision", ids: []string{"101"}, wantError: true, wantItems: 1, mutate: func(t *testing.T, _ EmbyWebhookRecord) {
			if err := db.Db.Model(&EmbyItemEvidence{}).Where("id > 0").Update("identity_key", "restored-other-object").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "server replaced at same URL", ids: []string{"101"}, wantError: true, wantItems: 1, mutate: func(t *testing.T, _ EmbyWebhookRecord) {
			if err := db.Db.Model(&EmbyIndexState{}).Where("id = 1").Update("server_id", "server-b").Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
				t.Fatal(err)
			}
			record := webhookModelClaim(t)
			if tc.mutate != nil {
				tc.mutate(t, record)
			}
			if err := FinalizeEmbyWebhookLocal(t.Context(), record, tc.ids); (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
			assertWebhookCount(t, &EmbyMediaItem{}, tc.wantItems)
			assertWebhookCount(t, &EmbyMediaSyncFile{}, tc.wantItems)
			assertWebhookCount(t, &EmbyItemEvidence{}, 1)
			assertWebhookCount(t, &SyncFile{}, 1)
		})
	}
}

func TestEmbyWebhookOversizeDiagnosticsRemainBackupSafe(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	plan := webhookModelPlan(t, record)
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	reason := strings.Repeat("错误\n", 2<<20)
	if err := SaveEmbyWebhookResult(t.Context(), record, EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionFailed, Reason: reason}); err != nil {
		t.Fatal(err)
	}
	if err := FinishEmbyWebhook(t.Context(), record, EmbyWebhookUnresolved, reason, 0, true); err != nil {
		t.Fatal(err)
	}
	var saved EmbyWebhookRecord
	if err := db.Db.First(&saved, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	targets, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil || len(targets) != 1 {
		t.Fatalf("targets=%+v err=%v", targets, err)
	}
	for _, value := range []string{saved.Reason, targets[0].Reason} {
		if len(value) > 4096 || !utf8.ValidString(value) || !strings.HasSuffix(value, "…") {
			t.Fatalf("unbounded or invalid diagnostic: bytes=%d", len(value))
		}
	}
	for _, value := range []any{saved, targets[0]} {
		encoded, err := json.Marshal(value)
		if err != nil || len(encoded) >= 16<<20 {
			t.Fatalf("backup JSONL limit exceeded: bytes=%d err=%v", len(encoded), err)
		}
	}
}

func TestEmbyWebhookObservationAdoptionRollsBackWithReceipt(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.new", file)); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyObservedEvidence(t.Context(), webhookModelClaim(t), token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("receipt failure after adoption")
	if err := db.Db.Callback().Create().Before("gorm:create").Register("test:adoption_receipt_fail", func(tx *gorm.DB) {
		if tx.Statement.Table == "emby_webhook_records" {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Db.Callback().Create().Remove("test:adoption_receipt_fail") })
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); !errors.Is(err, injected) {
		t.Fatal(err)
	}
	assertWebhookCount(t, &EmbyItemState{}, 0)
	assertWebhookCount(t, &EmbyItemEvidence{}, 1)
	assertWebhookCount(t, &EmbyMediaItem{}, 0)
	assertWebhookCount(t, &EmbyMediaSyncFile{}, 0)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal("rolled-back receipt invalidated token", err)
	}
}

func TestEmbyWebhookSavedSuccessCannotFinalizeReadmittedOrRestoredIdentity(t *testing.T) {
	for _, tc := range []struct {
		name      string
		mutate    func(*testing.T, EmbyWebhookRecord)
		wantError bool
	}{
		{name: "readmitted surviving item", mutate: func(t *testing.T, record EmbyWebhookRecord) {
			if err := AdmitEmbyWebhookSurvivors(t.Context(), record, []string{"101"}); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "restored evidence has same row ID and generation", wantError: true, mutate: func(t *testing.T, _ EmbyWebhookRecord) {
			if err := db.Db.Model(&EmbyItemEvidence{}).Where("id > 0").Update("identity_key", "another-restored-identity").Error; err != nil {
				t.Fatal(err)
			}
		}},
		{name: "connection changed before next sync", wantError: true, mutate: func(t *testing.T, _ EmbyWebhookRecord) {
			if err := db.Db.Model(&EmbyConfig{}).Where("id > 0").Update("emby_url", "https://another-emby.invalid").Error; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
				t.Fatal(err)
			}
			record := webhookModelClaim(t)
			plan := webhookModelPlan(t, record)
			if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
				t.Fatal(err)
			}
			result := EmbyDeletionResult{Key: plan.Targets[0].Key, Outcome: EmbyDeletionDeleted}
			if err := SaveEmbyWebhookResult(t.Context(), record, result); err != nil {
				t.Fatal(err)
			}
			tc.mutate(t, record)
			if err := FinalizeEmbyDeletionPlan(t.Context(), plan, []EmbyDeletionResult{result}); (err != nil) != tc.wantError {
				t.Fatalf("err=%v wantError=%v", err, tc.wantError)
			}
			assertWebhookCount(t, &EmbyMediaItem{}, 1)
			assertWebhookCount(t, &EmbyMediaSyncFile{}, 1)
		})
	}
}

func TestEmbyWebhookObservationRejectsSnapshotCommittedDuringRead(t *testing.T) {
	config, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.modified", file)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	beforeGET, err := BeginEmbyIndexRead("server-a", config)
	if err != nil {
		t.Fatal(err)
	}
	// 同一 STRM 路径换成新物理文件，另一同步在旧 GET 返回前已经提交。
	replacement := file
	replacement.BaseModel = BaseModel{}
	replacement.FileId, replacement.PickCode = "new-object", "new-pickcode"
	if err := db.Db.Create(&replacement).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(beforeGET, []EmbyItemSnapshot{snapshotForFile(replacement)}); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyObservedEvidence(t.Context(), record, beforeGET, []EmbyItemSnapshot{snapshotForFile(file)}); !errors.Is(err, ErrEmbySnapshotStale) {
		t.Fatalf("old GET became trusted evidence after newer item commit: %v", err)
	}
	deleted, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file))
	if err != nil {
		t.Fatal(err)
	}
	input := webhookModelInput(t, deleted)
	if len(input.Owners) != 1 || len(input.Owners[0].Files) != 1 || input.Owners[0].Files[0].FileID != "new-object" {
		t.Fatalf("new snapshot was replaced: %+v", input)
	}
}
