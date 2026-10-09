package emby

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"

	"qmediasync/internal/helpers"
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
		if strings.Contains(line, "准备联动删除：") {
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
