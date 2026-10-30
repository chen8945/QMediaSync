package logs

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
)

func TestTip和Progress通过QLogger输出并脱敏请求头(t *testing.T) {
	oldAppLogger := helpers.AppLogger
	oldLogLevel := helpers.ConfiguredLogLevel()
	t.Cleanup(func() {
		helpers.AppLogger = oldAppLogger
		helpers.SetGlobalLogLevel(oldLogLevel)
	})

	helpers.SetGlobalLogLevel(helpers.LogLevelInfo)
	var output bytes.Buffer
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(&output, "", 0)}

	headers := "Authorization=Bearer auth-secret; X-Emby-Token=emby-token; Cookie=session-secret; User-Agent=QMediaSync/1.0"
	Tip("参与 cache key 计算的请求头: %s", headers)
	Progress("辅助 Progress 进度记录发送成功: %s", headers)

	got := output.String()
	for _, secret := range []string{"auth-secret", "emby-token", "session-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("日志仍包含敏感值 %q: %s", secret, got)
		}
	}
	for _, want := range []string{
		"[INFO] 参与 cache key 计算的请求头:",
		"[INFO] 辅助 Progress 进度记录发送成功:",
		"User-Agent=QMediaSync/1.0",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("日志缺少 %q: %s", want, got)
		}
	}
	for _, unwanted := range []string{"[SUCCESS]", "[PROGRESS]", "\x1b["} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("日志不应包含 %q: %q", unwanted, got)
		}
	}
}
