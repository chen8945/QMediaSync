package emby

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func legacyWebhookJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

// 仅替换历史关联 ID 的 JSON 类型，其余冻结字段不经过浮点数转换。
func legacyWebhookInputJSON(t *testing.T, raw string) string {
	t.Helper()
	var input map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	var owners []map[string]json.RawMessage
	if err := json.Unmarshal(input["owners"], &owners); err != nil {
		t.Fatal(err)
	}
	converted := 0
	for _, owner := range owners {
		var links []map[string]json.RawMessage
		if err := json.Unmarshal(owner["links"], &links); err != nil {
			t.Fatal(err)
		}
		for _, link := range links {
			var id string
			if err := json.Unmarshal(link["emby_item_id"], &id); err != nil {
				t.Fatal(err)
			}
			numeric, err := strconv.ParseUint(id, 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			link["emby_item_id"] = json.RawMessage(strconv.FormatUint(numeric, 10))
			converted++
		}
		owner["links"] = legacyWebhookJSON(t, links)
	}
	if converted == 0 {
		t.Fatal("fixture has no link identity to convert")
	}
	input["owners"] = legacyWebhookJSON(t, owners)
	return string(legacyWebhookJSON(t, input))
}

func legacyWebhookPlanJSON(t *testing.T, raw string) string {
	t.Helper()
	var plan map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &plan); err != nil {
		t.Fatal(err)
	}
	plan["input"] = json.RawMessage(legacyWebhookInputJSON(t, string(plan["input"])))
	return string(legacyWebhookJSON(t, plan))
}

func TestWebhookLegacyIntegerInputJSON(t *testing.T) {
	for _, name := range []string{"authorized", "local_only"} {
		t.Run(name, func(t *testing.T) {
			f := setupCleanupWorkerTest(t, "Movie", false)
			if name == "local_only" {
				if err := db.Db.Model(&f.config).Update("enable_delete_netdisk", 0).Error; err != nil {
					t.Fatal(err)
				}
			}
			record := f.receive(t)
			raw := legacyWebhookInputJSON(t, record.InputJSON)
			if err := db.Db.Model(&record).Update("input_json", raw).Error; err != nil {
				t.Fatal(err)
			}
			f.worker.process(t.Context(), claimWebhookTest(t))
			assertCleanupWorkerDone(t, record.ID)
			fresh := readWebhookTest(t, record.ID)
			if fresh.InputJSON != raw {
				t.Fatal("processing rewrote frozen legacy input")
			}
			countWebhookTest(t, &models.EmbyMediaItem{}, 0)
			countWebhookTest(t, &models.EmbyMediaSyncFile{}, 0)
			countWebhookTest(t, &models.EmbyItemEvidence{}, 1)
			countWebhookTest(t, &models.SyncFile{}, 3)
			if name == "local_only" {
				if fresh.Authorized || fresh.PlanJSON != "" || len(f.cloud.batchCalls) != 0 || len(f.cloud.files) != 3 {
					t.Fatal("local-only legacy receipt acquired cloud deletion authority")
				}
				countWebhookTest(t, &models.EmbyWebhookTarget{}, 0)
			} else {
				if fresh.PlanJSON == "" || len(f.cloud.batchCalls) != 1 || len(f.cloud.files) != 0 {
					t.Fatalf("legacy input did not complete one batch: calls=%v remaining=%v", f.cloud.batchCalls, f.cloud.files)
				}
				ids := slices.Clone(f.cloud.batchCalls[0])
				slices.Sort(ids)
				if !slices.Equal(ids, []string{"f1", "image", "nfo"}) {
					t.Fatalf("unexpected deletion targets: %v", ids)
				}
				rows := cleanupWorkerRows(t, record.ID)
				if len(rows) != 3 {
					t.Fatalf("target count=%d, want 3", len(rows))
				}
				for id, row := range rows {
					if row.Outcome != models.EmbyDeletionDeleted || row.Attempts != 1 {
						t.Fatalf("target %s outcome=%s attempts=%d", id, row.Outcome, row.Attempts)
					}
				}
			}
			if len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 {
				t.Fatal("legacy input changed deletion scope")
			}
		})
	}
}

func TestWebhookLegacyIntegerPlanResume(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	f.cloud.beforeBatch = func(_ context.Context, files []models.EmbyFrozenFile) (bool, error) {
		for _, file := range files {
			if file.FileID != "f1" {
				delete(f.cloud.files, file.FileID)
			}
		}
		return false, errors.New("partial provider failure")
	}
	record := f.receive(t)
	f.worker.process(t.Context(), claimWebhookTest(t))
	first := readWebhookTest(t, record.ID)
	rows := cleanupWorkerRows(t, record.ID)
	if first.Status != models.EmbyWebhookRetry || rows["f1"].Outcome != models.EmbyDeletionFailed || rows["nfo"].Outcome != models.EmbyDeletionDeleted || rows["image"].Outcome != models.EmbyDeletionDeleted {
		t.Fatal("fixture did not persist partial completion")
	}
	rawInput := legacyWebhookInputJSON(t, first.InputJSON)
	rawPlan := legacyWebhookPlanJSON(t, first.PlanJSON)
	if err := db.Db.Model(&record).Updates(map[string]any{"input_json": rawInput, "plan_json": rawPlan}).Error; err != nil {
		t.Fatal(err)
	}
	interrupted := claimWebhookTest(t)
	if err := models.RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.cloud.beforeBatch = nil
	recovered := claimWebhookTest(t)
	if recovered.ClaimToken == interrupted.ClaimToken || recovered.Attempts != first.Attempts {
		t.Fatal("restart failed to retain attempts and replace the claim")
	}
	f.worker.process(t.Context(), recovered)
	assertCleanupWorkerDone(t, record.ID)
	fresh := readWebhookTest(t, record.ID)
	if fresh.InputJSON != rawInput || fresh.PlanJSON != rawPlan {
		t.Fatal("resume rewrote frozen legacy input or plan")
	}
	if len(f.cloud.batchCalls) != 2 || !slices.Equal(f.cloud.batchCalls[1], []string{"f1"}) || len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 || len(f.cloud.files) != 0 {
		t.Fatalf("resume resent completed targets or changed scope: batches=%v", f.cloud.batchCalls)
	}
	rows = cleanupWorkerRows(t, record.ID)
	if rows["f1"].Attempts != 2 || rows["nfo"].Attempts != 1 || rows["image"].Attempts != 1 {
		t.Fatal("resume changed completed target attempts")
	}
	for id, row := range rows {
		if row.Outcome != models.EmbyDeletionDeleted {
			t.Fatalf("target %s remained %s", id, row.Outcome)
		}
	}
	countWebhookTest(t, &models.EmbyMediaItem{}, 0)
	countWebhookTest(t, &models.EmbyMediaSyncFile{}, 0)
}

func TestWebhookLegacyIntegerPlanSaveImmutable(t *testing.T) {
	f := setupCleanupWorkerTest(t, "Movie", false)
	f.receive(t)
	record := claimWebhookTest(t)
	var input models.EmbyDeletionInput
	if err := json.Unmarshal([]byte(record.InputJSON), &input); err != nil {
		t.Fatal(err)
	}
	if err := input.BindMediaServerType(record.MediaServerType); err != nil {
		t.Fatal(err)
	}
	plan, err := models.BuildEmbyDeletionPlan(t.Context(), input, f.worker.provider, f.worker.verify)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Targets) != 3 {
		t.Fatalf("fixture target count=%d, want 3", len(plan.Targets))
	}
	if err := models.SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	current := readWebhookTest(t, record.ID)
	rawInput := legacyWebhookInputJSON(t, current.InputJSON)
	rawPlan := legacyWebhookPlanJSON(t, current.PlanJSON)
	if err := db.Db.Model(&record).Updates(map[string]any{"input_json": rawInput, "plan_json": rawPlan}).Error; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := models.SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
			t.Fatalf("equivalent decoded plan was rejected: %v", err)
		}
	}
	stored := readWebhookTest(t, record.ID)
	if stored.InputJSON != rawInput || stored.PlanJSON != rawPlan {
		t.Fatal("saving equivalent plan rewrote frozen JSON")
	}
	before, err := models.LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != 3 {
		t.Fatalf("idempotent save duplicated targets: %d", len(before))
	}
	for _, mutation := range []struct {
		name  string
		apply func(*models.EmbyDeletionPlan)
	}{
		{"link_identity", func(p *models.EmbyDeletionPlan) { p.Input.Owners[0].Links[0].EmbyItemId = "202" }},
		{"generation", func(p *models.EmbyDeletionPlan) { p.Input.Owners[0].Evidence.Generation++ }},
		{"authorization", func(p *models.EmbyDeletionPlan) { p.Input.Authorized = false }},
		{"target_identity", func(p *models.EmbyDeletionPlan) {
			p.Targets[0].File.FileID = "replacement"
			p.Targets[0].Key = models.EmbyDeletionTargetKey(p.Targets[0])
		}},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			var forged models.EmbyDeletionPlan
			if err := json.Unmarshal(legacyWebhookJSON(t, plan), &forged); err != nil {
				t.Fatal(err)
			}
			mutation.apply(&forged)
			if err := models.SaveEmbyWebhookPlan(t.Context(), record, forged); !errors.Is(err, models.ErrEmbyIdentityAmbiguous) {
				t.Fatalf("tampered plan error=%v, want identity rejection", err)
			}
			fresh := readWebhookTest(t, record.ID)
			after, err := models.LoadEmbyWebhookTargets(t.Context(), record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if fresh.InputJSON != stored.InputJSON || fresh.PlanJSON != stored.PlanJSON || !slices.Equal(before, after) {
				t.Fatal("rejected mutation changed frozen input, plan or targets")
			}
		})
	}
	if len(f.cloud.batchCalls) != 0 || len(f.cloud.scalarCalls) != 0 || f.cloud.directoryCalls != 0 {
		t.Fatal("saving a plan performed a deletion")
	}
}
