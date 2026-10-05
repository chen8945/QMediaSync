package models

import (
	"errors"
	"testing"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestEmbyWebhookPlanInitialAbsenceIsAtomicAndSurvivesRecovery(t *testing.T) {
	_, token, file := setupEmbyWebhookModelTest(t)
	if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
		t.Fatal(err)
	}
	record := webhookModelClaim(t)
	plan := webhookModelPlan(t, record)
	absent := []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionAlreadyAbsent}}
	assertEmbyWebhookInitialAbsencePersistence(t, record, plan, absent, nil)
}

func assertEmbyWebhookInitialAbsencePersistence(t *testing.T, record EmbyWebhookRecord, plan EmbyDeletionPlan, absent []EmbyDeletionResult, reopen func()) {
	t.Helper()
	conn := db.Db
	injected := errors.New("initial absence plan write failed")
	if err := conn.Callback().Update().Before("gorm:update").Register("test:initial-absence-plan", func(tx *gorm.DB) {
		updates, ok := tx.Statement.Dest.(map[string]any)
		if ok && tx.Statement.Table == "emby_webhook_records" && updates["plan_json"] != nil {
			tx.AddError(injected)
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Callback().Update().Remove("test:initial-absence-plan") })
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan, absent...); !errors.Is(err, injected) {
		t.Fatalf("expected final plan write failure, got %v", err)
	}
	assertWebhookCount(t, &EmbyWebhookTarget{}, 0)
	var saved EmbyWebhookRecord
	if err := db.Db.First(&saved, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.PlanJSON != "" {
		t.Fatal("failed initial absence left a partial plan")
	}
	if err := conn.Callback().Update().Remove("test:initial-absence-plan"); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan, absent...); err != nil {
		t.Fatal(err)
	}
	// 计划提交后、worker 收尾前重启，候选目标已经是终态，不会进入删除路径。
	if reopen != nil {
		reopen()
	}
	if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := webhookModelClaim(t)
	rows, err := LoadEmbyWebhookTargets(t.Context(), record.ID)
	if err != nil || len(rows) != len(plan.Targets) {
		t.Fatalf("recovered targets: %+v err=%v", rows, err)
	}
	if recovered.PlanJSON != embyJSON(plan) || recovered.ClaimToken == record.ClaimToken {
		t.Fatalf("initial absence was not durable: record=%+v targets=%+v", recovered, rows)
	}
	for _, row := range rows {
		if row.Outcome != EmbyDeletionAlreadyAbsent || row.Attempts != 0 {
			t.Fatalf("initial absence became an executable pending target: %+v", row)
		}
	}
}

func TestEmbyWebhookPlanRejectsInvalidInitialAbsence(t *testing.T) {
	for _, name := range []string{"deleted", "failed", "unresolved", "reason", "unknown_key", "duplicate", "directory", "unknown_kind", "target_reason", "file_reason", "old_policy", "lost_claim"} {
		t.Run(name, func(t *testing.T) {
			_, token, file := setupEmbyWebhookModelTest(t)
			if err := ApplyEmbySnapshots(token, []EmbyItemSnapshot{snapshotForFile(file)}); err != nil {
				t.Fatal(err)
			}
			if _, err := SaveEmbyWebhook(t.Context(), webhookModelEnvelope("library.deleted", file)); err != nil {
				t.Fatal(err)
			}
			record := webhookModelClaim(t)
			plan := webhookModelPlan(t, record)
			absent := []EmbyDeletionResult{{Key: plan.Targets[0].Key, Outcome: EmbyDeletionAlreadyAbsent}}
			switch name {
			case "deleted", "failed", "unresolved":
				absent[0].Outcome = EmbyDeletionOutcome(name)
			case "reason":
				absent[0].Reason = "unverified"
			case "unknown_key":
				absent[0].Key = "unknown"
			case "duplicate":
				absent = append(absent, absent[0])
			case "directory":
				plan.Targets[0].Kind = "directory"
			case "unknown_kind":
				plan.Targets[0].Kind = "unknown"
			case "target_reason":
				plan.Targets[0].Reason = "unverified"
			case "file_reason":
				plan.Targets[0].File.Reason = "unverified"
			case "old_policy":
				plan.Input.CleanupPolicy = EmbyCleanupPolicy{}
				if err := db.Db.Model(&EmbyWebhookRecord{}).Where("id = ?", record.ID).Update("input_json", embyJSON(plan.Input)).Error; err != nil {
					t.Fatal(err)
				}
			case "lost_claim":
				record.ClaimToken = "lost"
			}
			err := SaveEmbyWebhookPlan(t.Context(), record, plan, absent...)
			if !errors.Is(err, ErrEmbyIdentityAmbiguous) && !(name == "lost_claim" && errors.Is(err, ErrEmbyWebhookClaimLost)) {
				t.Fatalf("invalid initial result accepted: %v", err)
			}
			assertWebhookCount(t, &EmbyWebhookTarget{}, 0)
			var saved EmbyWebhookRecord
			if err := db.Db.First(&saved, record.ID).Error; err != nil {
				t.Fatal(err)
			}
			if saved.PlanJSON != "" {
				t.Fatal("invalid initial result persisted a plan")
			}
		})
	}
}
