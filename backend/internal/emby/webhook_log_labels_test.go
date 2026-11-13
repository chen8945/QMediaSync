package emby

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestEmbyDeletionSupplementRetentionLabelGroupsReasons(t *testing.T) {
	issues := []string{
		"source_unresolved:source_account_changed:source_0:101",
		"source_unresolved:source_account_changed:source_1:102",
		"source_unresolved:strm_reference_unverified:source_0:103",
		"source_unresolved:source_identity_conflict:source_0:104",
		"source_unresolved:source_lookup_retries_exhausted:source_0:105",
		"source_unresolved:https://private.invalid/file?token=secret:106",
	}
	label := embyDeletionSupplementRetentionLabel(issues)
	for _, want := range []string{
		"6 个来源已保留", "网盘账号身份已变化：2 个", "STRM 路径映射已变化或无法确认：1 个",
		"文件身份与已保存记录不一致：1 个", "来源信息仍无法确认：1 个",
		fmt.Sprintf("已用完 %d 次处理机会并停止本次重试：1 个", webhookMaxAttempts),
	} {
		if !strings.Contains(label, want) {
			t.Errorf("汇总缺少 %q：%s", want, label)
		}
	}
	if strings.Count(label, "网盘账号身份已变化") != 1 {
		t.Fatalf("相同保留原因没有合并计数：%s", label)
	}
	for _, unwanted := range []string{"source_", "private.invalid", "token", "secret", "http", "\n"} {
		if strings.Contains(label, unwanted) {
			t.Errorf("汇总泄露来源或内部字段 %q：%s", unwanted, label)
		}
	}
	if got := embyDeletionSupplementRetentionLabel(nil); got != "" {
		t.Fatalf("无保留来源却追加了说明：%s", got)
	}
}

func captureEmbySupplementLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var output bytes.Buffer
	previous := helpers.AppLogger
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}
	t.Cleanup(func() { helpers.AppLogger = previous })
	return &output
}

func assertEmbySupplementSummary(t *testing.T, output string, reason string) {
	t.Helper()
	var summaries []string
	for line := range strings.SplitSeq(output, "\n") {
		if strings.Contains(line, "联动删除：正在核验待删除文件") {
			summaries = append(summaries, line)
		}
	}
	if len(summaries) != 1 || !strings.Contains(summaries[0], "1 个来源已保留") || !strings.Contains(summaries[0], reason) {
		t.Fatalf("准备日志未按原因集中说明保留来源：%q", summaries)
	}
	for _, unwanted := range []string{"source_", "http://", "https://", "external-", "conflicting-", "unavailable-"} {
		if strings.Contains(summaries[0], unwanted) {
			t.Errorf("准备日志泄露来源或内部码 %q：%s", unwanted, summaries[0])
		}
	}
}

func TestWebhookRecordLabelUsesOnlyMatchingFrozenDisplayHistory(t *testing.T) {
	season, episode, special := 1, 6, 0
	primary := models.EmbyMediaItem{ItemId: "episode", ServerId: "server", Type: "Episode", Name: "原单集名", SeriesId: "series", SeriesName: "记忆管理局", SeasonId: "season", ParentIndexNumber: 1, IndexNumber: 6}
	owner := func(item models.EmbyMediaItem) models.EmbyDeletionOwner { return models.EmbyDeletionOwner{Item: item} }
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		for _, tc := range []struct {
			name, itemID, itemType, want string
			envelope                     models.EmbyWebhookEnvelope
			items                        []models.EmbyMediaItem
			alter                        func(*models.EmbyWebhookRecord)
		}{
			{name: "episode_missing_season", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{SeriesName: "记忆管理局", IndexNumber: &episode}, items: []models.EmbyMediaItem{primary}, want: "记忆管理局 S01E06"},
			{name: "existing_payload_names_preserved", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{ItemName: "新生命周期的名", SeriesName: "新剧名"}, items: []models.EmbyMediaItem{primary}, want: "新剧名 S01E06"},
			{name: "season_missing_series", itemID: "season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "第 1 季", ParentIndexNumber: &season}, items: []models.EmbyMediaItem{primary}, want: "记忆管理局 S01"},
			{name: "hidden_part_defaults_ignored", itemID: "season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "第 1 季", ParentIndexNumber: &season}, items: []models.EmbyMediaItem{{ItemId: "part", ServerId: "server", Type: "Video", SeasonId: "season"}, primary}, want: "记忆管理局 S01"},
			{name: "series", itemID: "series", itemType: "Series", items: []models.EmbyMediaItem{primary}, want: "记忆管理局"},
			{name: "explicit_special_season", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{SeriesName: "记忆管理局", ParentIndexNumber: &special, IndexNumber: &episode}, want: "记忆管理局 S00E06"},
			{name: "unrelated_season", itemID: "other-season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "第 1 季", ParentIndexNumber: &season}, items: []models.EmbyMediaItem{primary}, want: "S01"},
			{name: "ambiguous_series_name", itemID: "season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "第 1 季", ParentIndexNumber: &season}, items: []models.EmbyMediaItem{primary, {ItemId: "other", ServerId: "server", Type: "Episode", SeasonId: "season", SeriesName: "另一剧", ParentIndexNumber: 1}}, want: "S01"},
			{name: "ambiguous_season_number_payload_known", itemID: "season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "第 1 季", ParentIndexNumber: &season}, items: []models.EmbyMediaItem{primary, {ItemId: "other", ServerId: "server", Type: "Episode", SeasonId: "season", SeriesName: "记忆管理局", ParentIndexNumber: 2}}, want: "记忆管理局 S01"},
			{name: "ambiguous_season_number_missing", itemID: "season", itemType: "Season", envelope: models.EmbyWebhookEnvelope{ItemName: "可靠原通知名"}, items: []models.EmbyMediaItem{primary, {ItemId: "other", ServerId: "server", Type: "Episode", SeasonId: "season", SeriesName: "记忆管理局", ParentIndexNumber: 2}}, want: "记忆管理局 可靠原通知名"},
			{name: "payload_season_not_overwritten_by_default", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{SeriesName: "记忆管理局", ParentIndexNumber: &season, IndexNumber: &episode}, items: []models.EmbyMediaItem{{ItemId: "episode", ServerId: "server", Type: "Episode", SeasonId: "season", ParentIndexNumber: 0}}, want: "记忆管理局 S01E06"},
			{name: "history_name_sanitized", itemID: "episode", itemType: "Episode", items: []models.EmbyMediaItem{{ItemId: "episode", ServerId: "server", Type: "Episode", SeriesName: "剧\nhttps://user:password@example.test/x?token=secret", SeasonId: "season", ParentIndexNumber: 1, IndexNumber: 6}}, want: "剧https://example.test/x S01E06"},
			{name: "wrong_server", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{ItemName: "原通知名"}, items: []models.EmbyMediaItem{primary}, alter: func(r *models.EmbyWebhookRecord) { r.ServerID = "other" }, want: "原通知名"},
			{name: "wrong_item", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{ItemName: "原通知名"}, items: []models.EmbyMediaItem{primary}, alter: func(r *models.EmbyWebhookRecord) { r.ItemID = "other" }, want: "原通知名"},
			{name: "wrong_connection", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{ItemName: "原通知名"}, items: []models.EmbyMediaItem{primary}, alter: func(r *models.EmbyWebhookRecord) { r.ServerConfigKey = "other" }, want: "原通知名"},
			{name: "broken_input", itemID: "episode", itemType: "Episode", envelope: models.EmbyWebhookEnvelope{ItemName: "原通知名"}, items: []models.EmbyMediaItem{primary}, alter: func(r *models.EmbyWebhookRecord) { r.InputJSON = "{" }, want: "原通知名"},
		} {
			t.Run(string(kind)+"/"+tc.name, func(t *testing.T) {
				input := models.EmbyDeletionInput{ServerID: "server", ServerConfigKey: "connection", ItemID: tc.itemID, ItemType: tc.itemType}
				for _, item := range tc.items {
					input.Owners = append(input.Owners, owner(item))
				}
				payload, _ := json.Marshal(tc.envelope)
				frozen, _ := json.Marshal(input)
				record := models.EmbyWebhookRecord{MediaServerType: kind, ServerID: "server", ServerConfigKey: "connection", Event: "library.deleted", ItemID: tc.itemID, ItemType: tc.itemType, PayloadJSON: string(payload), InputJSON: string(frozen)}
				if tc.alter != nil {
					tc.alter(&record)
				}
				before := record
				if got := embyWebhookRecordLabel(record); got != tc.want {
					t.Fatalf("label=%q want %q", got, tc.want)
				}
				if record != before {
					t.Fatal("display formatting changed the frozen receipt")
				}
			})
		}
	}
}

func TestWebhookCompletionLogsNameIDAndActualAction(t *testing.T) {
	for _, kind := range []models.MediaServerType{models.MediaServerEmby, models.MediaServerJellyfin} {
		for _, tc := range []struct{ event, status, reason, action string }{
			{"library.new", models.EmbyWebhookDone, "", "条目同步已完成"},
			{"library.modified", models.EmbyWebhookRetry, "暂时不可达", "条目同步稍后自动重试"},
			{"library.deleted", models.EmbyWebhookDone, "", "删除处理已完成"},
			{"deep.delete", models.EmbyWebhookUnresolved, "证据不足", "删除处理未完成（已停止自动重试）"},
			{"library.deleted", models.EmbyWebhookDone, "received_without_deletion_authorization", "删除处理已完成"},
			{"library.new", models.EmbyWebhookDone, "nonphysical_item_skipped", "条目同步已跳过，无需建立物理文件索引"},
		} {
			t.Run(string(kind)+"/"+tc.event+"/"+tc.reason, func(t *testing.T) {
				setupWebhookWorkerTest(t, false)
				output := captureEmbySupplementLogs(t)
				level := helpers.ConfiguredLogLevel()
				helpers.SetGlobalLogLevel(helpers.LogLevelDebug)
				t.Cleanup(func() { helpers.SetGlobalLogLevel(level) })
				season, episode := 1, 4
				payload, _ := json.Marshal(models.EmbyWebhookEnvelope{ItemName: "单集标题", SeriesName: "记忆管理局", ParentIndexNumber: &season, IndexNumber: &episode})
				record := models.EmbyWebhookRecord{MediaServerType: kind, Event: tc.event, ItemID: "101", PayloadJSON: string(payload), Status: models.EmbyWebhookRunning, ClaimToken: "claim", InputJSON: "frozen-input"}
				if err := db.Db.Create(&record).Error; err != nil {
					t.Fatal(err)
				}
				if err := finishEmbyWebhook(t.Context(), record, tc.status, tc.reason, 0, false); err != nil {
					t.Fatal(err)
				}
				want := kind.DisplayName() + " 媒体通知：记忆管理局 S01E04（ItemId 101），" + tc.action
				if !strings.Contains(output.String(), want) || strings.Contains(output.String(), "网盘已删除") {
					t.Fatalf("wrong action or identifier placement: %s", output.String())
				}
				if tc.reason == "nonphysical_item_skipped" && (strings.Contains(output.String(), "条目同步已完成") || !strings.Contains(output.String(), "DEBUG")) {
					t.Fatalf("nonphysical skip was reported as a completed sync: %s", output.String())
				}
				if tc.reason == "received_without_deletion_authorization" && !strings.Contains(output.String(), "不删文件") {
					t.Fatalf("disabled deletion was not explained: %s", output.String())
				}
				var stored models.EmbyWebhookRecord
				if err := db.Db.First(&stored, record.ID).Error; err != nil || stored.Status != tc.status || stored.InputJSON != record.InputJSON || stored.Authorized {
					t.Fatalf("formatting changed receipt authority/history: %+v err=%v", stored, err)
				}
			})
		}
	}
}
