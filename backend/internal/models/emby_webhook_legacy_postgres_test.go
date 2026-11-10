//go:build integration

package models

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestEmbyWebhookPostgresLegacyLinkJSONRecovery(t *testing.T) {
	conn, schema, _, _, _ := setupEmbyWebhookPostgres(t)
	if _, err := SaveEmbyWebhook(t.Context(), embyPostgresEnvelope("library.deleted", "9101")); err != nil {
		t.Fatal(err)
	}
	record := claimEmbyWebhookPostgres(t, time.Now().Unix())
	inputJSON := strings.ReplaceAll(record.InputJSON, `"emby_item_id":"9101"`, `"emby_item_id":9101`)
	if inputJSON == record.InputJSON {
		t.Fatal("测试收件缺少旧数字关联")
	}
	if err := conn.Model(&record).Update("input_json", inputJSON).Error; err != nil {
		t.Fatal(err)
	}
	record.InputJSON = inputJSON
	plan := embyWebhookPlanPostgres(t, record)
	if err := SaveEmbyWebhookPlan(t.Context(), record, plan); err != nil {
		t.Fatal(err)
	}
	planJSON := strings.ReplaceAll(embyJSON(plan), `"emby_item_id":"9101"`, `"emby_item_id":9101`)
	if err := conn.Model(&record).Update("plan_json", planJSON).Error; err != nil {
		t.Fatal(err)
	}
	conn = reopenEmbyWebhookPostgres(t, conn, schema)
	if err := RecoverEmbyWebhookWork(t.Context()); err != nil {
		t.Fatal(err)
	}
	recovered := claimEmbyWebhookPostgres(t, time.Now().Unix())
	if recovered.InputJSON != inputJSON || recovered.PlanJSON != planJSON || recovered.ClaimToken == record.ClaimToken {
		t.Fatal("重开连接后冻结 JSON 被改写或领取未恢复")
	}
	var savedPlan EmbyDeletionPlan
	if err := json.Unmarshal([]byte(recovered.PlanJSON), &savedPlan); err != nil {
		t.Fatal(err)
	}
	if err := SaveEmbyWebhookPlan(t.Context(), recovered, savedPlan); err != nil {
		t.Fatal(err)
	}
	var saved EmbyWebhookRecord
	if err := conn.First(&saved, recovered.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.InputJSON != inputJSON || saved.PlanJSON != planJSON {
		t.Fatal("重复保存改写了冻结 JSON")
	}
}
