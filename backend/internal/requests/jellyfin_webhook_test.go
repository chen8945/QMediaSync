package requests

import (
	"strings"
	"testing"
)

// v21/v22 标准输出均没有 Path/ParentId；保留代表性真实 Name 转义。
const jellyfinStandardWebhook = `{"ServerId":"6db535cee3fa457898391802f1c67f45","ServerName":"QMS isolated research","ServerVersion":"10.11.11","ServerUrl":"http://display.invalid","NotificationType":"ItemAdded","Timestamp":"2026-11-11T07:22:07.0848617+00:00","UtcTimestamp":"2026-11-11T07:22:07.0848637Z","Name":"中文 \\\"引号\\\" \\ 路径\n换行","Overview":"synthetic webhook fixture only","Tagline":"","ItemId":"0886c67586e970b3684cd408cf1bdeca","ItemType":"Movie","RunTimeTicks":0,"RunTime":"00:00:00","Year":2026,"Provider_imdb":"tt0000000"}`

func TestJellyfinWebhookStandardPayload(t *testing.T) {
	for _, test := range []struct {
		name, kind, event string
		managed           bool
	}{
		{"added", "ItemAdded", "library.new", true},
		{"deleted", "ItemDeleted", "library.deleted", true},
		{"playback_start", "PlaybackStart", "playback.start", false},
		{"playback_stop", "PlaybackStop", "playback.stop", false},
		{"progress_has_no_pause_event", "PlaybackProgress", "", false},
		{"unsupported_update", "ItemUpdated", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := ParseJellyfinWebhook([]byte(strings.Replace(jellyfinStandardWebhook, "ItemAdded", test.kind, 1)))
			if err != nil {
				t.Fatal(err)
			}
			if request.Event() != test.event || request.Managed() != test.managed {
				t.Fatalf("错误事件映射：%+v", request)
			}
			envelope := request.ToEnvelope()
			if envelope.ItemPath != "" || envelope.ParentID != "" || len(envelope.Candidates) != 0 {
				t.Fatal("标准事件不能制造物理删除证据")
			}
			if request.Name != "中文 \"引号\" \\ 路径\n换行" || request.ServerId != "6db535cee3fa457898391802f1c67f45" {
				t.Fatalf("展示/身份字段被重复解码：%+v", request)
			}
		})
	}
	episode := `{"NotificationType":"PlaybackStop","ServerId":"server","ItemId":"0886C675-86E9-70B3-684C-D408CF1BDECA","ItemType":"Episode","SeriesId":"11111111-1111-1111-1111-111111111111","SeasonId":"22222222-2222-2222-2222-222222222222","SeasonNumber":1,"EpisodeNumber":2,"UserId":"33333333333333333333333333333333","NotificationUsername":"reader","DeviceName":"TV","Client":"Jellyfin","Id":"session","PlaybackPositionTicks":600000000,"RunTimeTicks":1200000000,"IsPaused":false}`
	request, err := ParseJellyfinWebhook([]byte(episode))
	if err != nil {
		t.Fatal(err)
	}
	playback := request.Playback()
	if request.ItemId != "0886c67586e970b3684cd408cf1bdeca" || playback.Item.SeasonNumber != 1 || playback.Item.EpisodeNumber != 2 || playback.PlaybackInfo.PlaySessionId != "session" || playback.GetPlaybackDuration() != 60000 {
		t.Fatalf("播放/季集字段丢失：%+v", playback)
	}
}

func TestJellyfinWebhookRejectsInvalidFields(t *testing.T) {
	for _, test := range []struct{ name, body string }{
		{"duplicate", `{"NotificationType":"ItemAdded","NotificationType":"ItemDeleted"}`},
		{"lowercase_null_string", `{"NotificationType":"system.test","name":null}`},
		{"lowercase_null_number", `{"NotificationType":"system.test","seasonnumber":null}`},
		{"null_pause", `{"NotificationType":"system.test","IsPaused":null}`},
		{"wrong_pause", `{"NotificationType":"system.test","IsPaused":"false"}`},
		{"null_name", `{"NotificationType":"system.test","Name":null}`},
		{"zero_guid", `{"NotificationType":"ItemDeleted","ItemType":"Movie","ItemId":"00000000-0000-0000-0000-000000000000"}`},
		{"invalid_parent_guid", `{"NotificationType":"ItemAdded","ItemType":"Episode","ItemId":"0886c67586e970b3684cd408cf1bdeca","SeriesId":"bad"}`},
		{"bad_unicode", `{"NotificationType":"system.test","Name":"\ud800"}`},
		{"multiple_objects", `{"NotificationType":"system.test"}{}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := ParseJellyfinWebhook([]byte(test.body)); err == nil {
				t.Fatal("无效字段被接受")
			}
		})
	}
	request, err := ParseJellyfinWebhook([]byte(`{"NotificationType":"ItemDeleted","ItemId":"0886c67586e970b3684cd408cf1bdeca","ItemType":"Movie"}`))
	if err != nil || !request.ToEnvelope().Blocked {
		t.Fatalf("缺真实ServerID应保留未核验状态：%+v %v", request, err)
	}
}

func TestJellyfinWebhookEnvelopeSeasonEpisodeLabels(t *testing.T) {
	for _, tc := range []struct{ name, fields, want string }{
		{"season", `"ItemType":"Season","Name":"第 1 季","SeriesName":"剧名","SeasonNumber":1`, "剧名 S01"},
		{"special_season", `"ItemType":"Season","SeriesName":"剧名","SeasonNumber":0`, "剧名 S00"},
		{"special_episode", `"ItemType":"Episode","SeriesName":"剧名","SeasonNumber":0,"EpisodeNumber":111`, "剧名 S00E111"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, err := ParseJellyfinWebhook([]byte(`{"NotificationType":"ItemDeleted","ServerId":"6db535cee3fa457898391802f1c67f45","ItemId":"0886c67586e970b3684cd408cf1bdeca",` + tc.fields + `}`))
			if err != nil {
				t.Fatal(err)
			}
			if got := request.ToEnvelope().DisplayLabel(); got != tc.want {
				t.Fatalf("label=%q want %q", got, tc.want)
			}
		})
	}
}
