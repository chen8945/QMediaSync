package emby

import (
	"fmt"
	"slices"
	"strings"

	"qmediasync/internal/models"
)

// embyDeletionOutcomeLabels 把删除结果的机器值翻译为面向用户的中文；数据库仍保存原值。
var embyDeletionOutcomeLabels = map[models.EmbyDeletionOutcome]string{
	models.EmbyDeletionDeleted:       "已删除",
	models.EmbyDeletionAlreadyAbsent: "网盘上已不存在（视为完成）",
	models.EmbyDeletionUnresolved:    "未能确认结果，已保留文件",
	models.EmbyDeletionFailed:        "删除失败，将自动重试",
}

// embyWebhookReasonLabels 把通知记录里保存的机器原因翻译为面向用户的中文。
var embyWebhookReasonLabels = map[string]string{
	"reused_confirmed_physical_generation":    "该文件此前已确认删除，跳过重复处理",
	"confirmed_directory_operation":           "已随确认的整目录删除一并清理",
	"missing_account":                         "缺少网盘账号信息",
	"missing_sync_root":                       "找不到对应的同步目录",
	"missing_file_identity":                   "缺少文件身份信息（文件 ID、大小等）",
	"missing_file_generation":                 "缺少该文件的历史版本记录",
	"account_identity_unverified":             "网盘账号身份未确认",
	"physical_path_unverified":                "网盘路径未能确认",
	"sync_scope_mismatch":                     "文件不在当前同步目录范围内",
	"original_directory_id_absent":            "原目录在网盘上已不存在",
	"received_without_deletion_authorization": "已收到通知；联动删除未开启，仅同步条目、不删文件",
	"deletion_disabled":                       "联动删除开关未开启",
	"unsupported_cleanup_policy":              "清理策略缺失或版本不受支持，已保留原始记录和文件",
	"invalid_metadata_evidence":               "元数据证据格式无效，已保留原始记录和文件",
	"sync_disabled_or_connection_changed":     "Emby 同步已停用或连接信息已变化，无法确认文件身份",
	"connection_changed":                      "Emby 连接信息已变化，无法确认文件身份",
	"server_instance_changed":                 "Emby 服务器标识已变化，无法确认是同一台服务器",
	"no_confirmed_historical_identity":        "没有可确认的历史身份记录，无法核实文件归属",
}

// embyWebhookStatusLabels 把通知工作状态翻译为面向用户的中文。
var embyWebhookStatusLabels = map[string]string{
	models.EmbyWebhookPending:    "等待处理",
	models.EmbyWebhookRunning:    "正在处理",
	models.EmbyWebhookRetry:      "稍后自动重试",
	models.EmbyWebhookUnresolved: "未完成（已停止自动重试）",
	models.EmbyWebhookDone:       "已完成",
}

// embyWebhookReasonTokens 按长度倒序排列，避免“connection_changed”截断“sync_disabled_or_connection_changed”。
var embyWebhookReasonTokens = func() []string {
	tokens := make([]string, 0, len(embyWebhookReasonLabels))
	for token := range embyWebhookReasonLabels {
		tokens = append(tokens, token)
	}
	slices.SortFunc(tokens, func(a, b string) int { return len(b) - len(a) })
	return tokens
}()

func embyDeletionOutcomeLabel(outcome models.EmbyDeletionOutcome) string {
	if label, ok := embyDeletionOutcomeLabels[outcome]; ok {
		return label
	}
	return string(outcome)
}

// embyWebhookReasonLabel 整串命中直接翻译；机器词嵌在错误文本里时做子串替换，未收录的原样保留。
func embyWebhookReasonLabel(reason string) string {
	for _, token := range embyWebhookReasonTokens {
		reason = strings.ReplaceAll(reason, token, embyWebhookReasonLabels[token])
	}
	return reason
}

func embyWebhookStatusLabel(status string) string {
	if label, ok := embyWebhookStatusLabels[status]; ok {
		return label
	}
	return status
}

// embyWebhookLogItem 拼接日志用 ItemId 标识，名称缺失时不输出空括号。
func embyWebhookLogItem(itemID, label string) string {
	if label == "" {
		return itemID
	}
	return fmt.Sprintf("%s（%s）", itemID, label)
}

// embyDeletionTargetNoun 返回日志用目标类型名词：目录目标显示“目录”，其余显示“文件”。
func embyDeletionTargetNoun(kind string) string {
	if kind == "directory" {
		return "目录"
	}
	return "文件"
}
