//go:build integration

package models

import (
	"context"
	"errors"
	"testing"
)

type embyInitialAbsentInventoryProvider struct{ *embyDeleteTestProvider }

func (embyInitialAbsentInventoryProvider) List(context.Context, EmbyFrozenFile) ([]EmbyRemoteFile, error) {
	return nil, errors.New("original parent inventory unavailable")
}

func TestEmbyWebhookPostgresInitialAbsenceIsAtomicAndSurvivesReopen(t *testing.T) {
	conn, schema, _, token, snapshot := setupEmbyWebhookPostgres(t)
	var metadata SyncFile
	if err := conn.Where("file_id = ?", "video-1").First(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	metadata.BaseModel = BaseModel{}
	metadata.FileId, metadata.FileName, metadata.PickCode = "nfo-1", "movie.nfo", ""
	metadata.LocalFilePath = "/local/media/movie.nfo"
	metadata.IsVideo, metadata.IsMeta = false, true
	if err := conn.Create(&metadata).Error; err != nil {
		t.Fatal(err)
	}
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshot}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	input := webhookModelInput(t, record)
	provider := embyInitialAbsentInventoryProvider{&embyDeleteTestProvider{}}
	factory := func(EmbyFrozenFile) (EmbyDeleteProvider, error) { return provider, nil }
	plan, err := BuildEmbyDeletionPlan(t.Context(), input, factory)
	if err != nil {
		t.Fatal(err)
	}
	plan, absent, err := ConfirmEmbyDeletionPlanInventory(t.Context(), plan, factory, allowEmbyDeleteTest)
	if err != nil || len(plan.Issues) != 0 || len(plan.Targets) != 2 || len(absent) != 2 {
		t.Fatalf("initial absent video and sidecar were not confirmed: plan=%+v absent=%+v err=%v", plan, absent, err)
	}
	assertEmbyWebhookInitialAbsencePersistence(t, record, plan, absent, func() {
		reopenEmbyWebhookPostgres(t, conn, schema)
	})
}
