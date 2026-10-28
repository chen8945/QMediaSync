package playback

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"
	"unicode/utf8"

	"qmediasync/internal/helpers"
)

func TestPreloadLogSuccessUsesFinalTargetWithoutDiagnostics(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output bytes.Buffer
		preloadLogger(t, &output, helpers.LogLevelInfo)
		p := newTestPreloader(t)
		candidate := preloadCandidate("resolved-target", PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
			task.SetLogTarget("episode-4", "剧名 S01E04")
			if !task.BeginSignature(preloadTestAccount, "resolved-target") || !task.Publish(func() {}) {
				t.Error("测试候选未发布")
			}
			return PreloadResult{Outcome: PreloadPublished, HTTPStatus: 200, BusinessCode: 1, StatusSource: "网盘"}
		})
		candidate.Log = PreloadLogContext{Entry: "Emby", Trigger: "播放接近结束", ItemID: "season", Title: "剧名 第1季"}
		p.Submit(candidate)
		synctest.Wait()
		got := output.String()
		for _, want := range []string{"[INFO] 预加载完成", "入口=Emby", "触发=播放接近结束", `目标="剧名 S01E04"`, "网盘=115", "耗时="} {
			if !strings.Contains(got, want) {
				t.Errorf("日志缺少 %q：%s", want, got)
			}
		}
		for _, unwanted := range []string{"任务=", "账号=", "尝试=", "HTTP=", "业务码=", "第1季"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("成功日志包含诊断或旧目标 %q：%s", unwanted, got)
			}
		}
		if strings.Count(got, "\n") != 1 {
			t.Fatalf("成功应只有一条日志：%s", got)
		}
	})
}

func TestPreloadLogReasonsAndStatusOrigins(t *testing.T) {
	for _, tc := range []struct {
		name   string
		event  preloadEvent
		want   []string
		absent []string
	}{
		{
			name:  "specific_debug_reason",
			event: preloadEvent{sequence: 3, level: helpers.LogLevelDebug, reason: "source_ambiguous"},
			want:  []string{"跳过预加载", "有多个播放源", "任务=3"}, absent: []string{"账号=0", "尝试=0", "HTTP=0", "业务码=0"},
		},
		{
			name:  "metadata_origin",
			event: preloadEvent{level: helpers.LogLevelDebug, reason: "metadata_request_failed", result: PreloadResult{HTTPStatus: 503, StatusSource: "Emby"}},
			want:  []string{"媒体资料查询失败", "Emby HTTP=503"}, absent: []string{"网盘 HTTP", "业务码=0"},
		},
		{
			name:  "known_provider_warning",
			event: preloadEvent{level: helpers.LogLevelWarn, reason: "risk", cooldown: 15 * time.Minute, account: 1, attempts: 1, result: PreloadResult{HTTPStatus: 429, BusinessCode: 70005, StatusSource: "网盘"}},
			want:  []string{"网盘请求受限", "账号=1", "网盘 HTTP=429", "网盘业务码=70005", "至少15分钟", "播放时仍按正常流程取链"}, absent: []string{"任务=", "尝试="},
		},
		{
			name:  "arbitrary_diagnostics_not_echoed",
			event: preloadEvent{level: helpers.LogLevelDebug, reason: "https://secret.invalid?token=unsafe", result: PreloadResult{HTTPStatus: 503, StatusSource: "external-secret"}},
			want:  []string{"尚未满足提前取链条件"}, absent: []string{"secret", "HTTP="},
		},
		{
			name:  "cache_reuse_is_debug",
			event: preloadEvent{level: helpers.LogLevelDebug, reason: "cached"},
			want:  []string{"无需重新取链", "已有可用缓存"}, absent: []string{"预加载完成"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := tc.event.message()
			for _, want := range tc.want {
				if !strings.Contains(message, want) {
					t.Errorf("缺少 %q：%s", want, message)
				}
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(message, unwanted) {
					t.Errorf("不应输出 %q：%s", unwanted, message)
				}
			}
		})
	}
}

func TestPreloadLogTargetSanitizationDoesNotChangeAdmission(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
		want  string
	}{
		{name: "empty", want: "条目 episode-1"},
		{name: "url", title: "影片 https://host.invalid/path?token=sentinel-secret", want: "条目 episode-1"},
		{name: "credential", title: "影片 api_key=sentinel-secret", want: "影片 api_key=******"},
		{name: "plain_token", title: "影片 Token = \"sentinel-secret\"", want: "条目 episode-1"},
		{name: "native_signature", title: "影片 authx: sentinel-secret", want: "条目 episode-1"},
		{name: "controls", title: "剧名\r\n\x1b\u202e S01E01", want: "剧名 S01E01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var output bytes.Buffer
				preloadLogger(t, &output, helpers.LogLevelInfo)
				p := newTestPreloader(t)
				candidate := preloadCandidate(tc.name, PreloadNext, func(_ context.Context, task *PreloadTask) PreloadResult {
					task.BeginSignature(preloadTestAccount, tc.name)
					task.Publish(func() {})
					return PreloadResult{Outcome: PreloadPublished}
				})
				candidate.Log = PreloadLogContext{Entry: "飞牛", Trigger: "单集详情", ItemID: "episode-1", Title: tc.title}
				if !p.Submit(candidate) {
					t.Fatal("日志名称不能影响候选准入")
				}
				synctest.Wait()
				got := output.String()
				if !strings.Contains(got, tc.want) || strings.Contains(got, "sentinel-secret") || strings.Contains(got, "https://") || strings.Count(got, "\n") != 1 {
					t.Fatalf("目标未正确净化或未发布：%s", got)
				}
			})
		})
	}
	long := safePreloadLogText(strings.Repeat("影", 10000), 160)
	if !utf8.ValidString(long) || len([]rune(long)) != 161 {
		t.Fatal("长标题必须有界且保持合法 UTF-8")
	}
	unsafe := preloadEvent{context: PreloadLogContext{Entry: "external-entry-secret", Trigger: "external-trigger-secret", Title: "title"}}.message()
	if strings.Contains(unsafe, "secret") {
		t.Fatal("入口和触发场景不能回显未知原文")
	}
}
