package playback

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"qmediasync/internal/helpers"
)

var preloadSensitiveName = regexp.MustCompile(`(?i)\b(?:token|auth_token|authx|sign|signature)\b["']?\s*[:=]`)

// PreloadLogContext 只复用已取得的展示资料，不参与选源或增加补查。
type PreloadLogContext struct {
	Entry   string
	Trigger string
	ItemID  string
	Title   string
}

func (c PreloadLogContext) bounded() PreloadLogContext {
	// Submit 也可能在适配器短锁内调用；此处只复制有界字段，净化留给 worker。
	c.Entry = strings.Clone(c.Entry[:min(len(c.Entry), 32)])
	c.Trigger = strings.Clone(c.Trigger[:min(len(c.Trigger), 64)])
	c.ItemID = strings.Clone(c.ItemID[:min(len(c.ItemID), 256)])
	c.Title = strings.Clone(c.Title[:min(len(c.Title), 2048)])
	return c
}

func (c PreloadLogContext) sanitized() PreloadLogContext {
	switch c.Entry {
	case "Emby", "飞牛":
	default:
		c.Entry = "未知入口"
	}
	switch c.Trigger {
	case "电影详情", "单集详情", "剧集页待播集", "季页待播集", "准备下一集", "播放接近结束", "准备下一分段":
	default:
		c.Trigger = ""
	}
	c.ItemID = safePreloadLogText(c.ItemID, 64)
	c.Title = safePreloadLogText(c.Title, 160)
	return c
}

func safePreloadLogText(value string, limit int) string {
	// 展示字段也来自外部响应；限制净化工作量，拒绝把地址当作名称输出。
	if len(value) > 2048 {
		value = value[:2048]
	}
	value = strings.Map(func(r rune) rune {
		if r == utf8.RuneError || unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, value)
	value = helpers.RedactSensitiveLog(value)
	if strings.Contains(value, "://") || strings.Contains(value, "//") || preloadSensitiveName.MatchString(value) {
		return ""
	}
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) > limit {
		value = string(runes[:limit]) + "…"
	}
	return value
}

// SetLogTarget 在唯一目标确定后更新日志展示；净化在协调器锁外完成。
func (t *PreloadTask) SetLogTarget(itemID, title string) {
	itemID, title = safePreloadLogText(itemID, 64), safePreloadLogText(title, 160)
	t.owner.mu.Lock()
	t.log.ItemID, t.log.Title = itemID, title
	t.owner.mu.Unlock()
}

func preloadReasonText(reason string) string {
	switch reason {
	case "metadata_budget":
		return "本轮资料查询额度已用完"
	case "metadata_request_failed":
		return "媒体资料查询失败"
	case "relation_unavailable":
		return "未能确定待播放的单集或分段"
	case "candidate_unknown":
		return "尚未确定可播放的影片或单集"
	case "warm_playbackinfo_already_tried":
		return "本候选已查询过播放源资料"
	case "source_origin_unknown":
		return "播放源不属于已配置的取链地址"
	case "provider_not_supported":
		return "该播放源暂不支持预加载"
	case "runtime_maintenance":
		return "系统正在维护，暂不预加载"
	case "playback_denied":
		return "当前账号暂不能播放该条目"
	case "source_unavailable":
		return "尚未取得可预加载的播放源"
	case "source_ambiguous":
		return "有多个播放源，尚未确定使用哪个"
	case "source_changed":
		return "播放源已变化"
	case "ua_unavailable":
		return "尚未取得可用于取链的播放器资料"
	case "account_unavailable":
		return "网盘账号不存在或授权暂不可用"
	case "account_lookup_failed":
		return "网盘账号资料查询失败"
	case "account_interval":
		return "同一网盘账号的取链间隔未到"
	case "account_busy":
		return "同一网盘账号正在处理其他预加载"
	case "source_busy":
		return "同一资源正在取链或已有其他播放占用"
	case "signature_unavailable":
		return "本轮暂不能开始网盘取链"
	case "cached":
		return "已有可用缓存"
	case "candidate_changed":
		return "当前候选已变化"
	case "playback_started":
		return "用户已开始播放，交由正常播放流程处理"
	case "canceled":
		return "当前详情或播放关联已失效"
	case "timeout":
		return "本轮后台执行时间已用完"
	case "transient_retry":
		return "网盘临时不可用，至少30秒后收到新信号时再试一次"
	case "timeout_provider":
		return "网盘连接超时"
	case "connection_failed":
		return "网盘连接暂不可用"
	case "risk":
		return "网盘请求受限"
	case "unauthorized":
		return "网盘授权已失效，请检查账号授权"
	case "invalid_link":
		return "网盘返回的链接无效或已过期"
	case "provider_failed":
		return "网盘未能返回可用链接"
	default:
		return "尚未满足提前取链条件"
	}
}

func (event preloadEvent) message() string {
	c := event.context.sanitized()
	if c.Trigger == "" {
		c.Trigger = "详情资料"
		if event.kind == PreloadNext {
			c.Trigger = "播放接近结束"
		}
	}
	target := c.Title
	if target == "" && c.ItemID != "" {
		target = "条目 " + c.ItemID
	}
	if target == "" {
		target = "未知条目"
	}
	action := "跳过预加载"
	switch event.reason {
	case "published":
		action = "预加载完成"
	case "cached":
		action = "无需重新取链"
	case "account_interval", "account_busy", "transient_retry":
		action = "等待预加载"
	case "canceled", "candidate_changed", "source_changed", "playback_started":
		action = "已取消预加载"
	default:
		if event.level == helpers.LogLevelWarn {
			action = "预加载暂未成功"
		}
	}
	message := fmt.Sprintf("%s：入口=%s，触发=%s，目标=%q", action, c.Entry, c.Trigger, target)
	switch event.provider {
	case "115":
		message += "，网盘=115"
	case "baidupan":
		message += "，网盘=百度网盘"
	}
	if event.level != helpers.LogLevelInfo {
		reason := preloadReasonText(event.reason)
		if event.reason == "transient_retry" && event.result.Reason != "" {
			reason = preloadReasonText(event.result.Reason) + "，至少30秒后收到新信号时再试一次"
		}
		message += "，原因=" + reason
		if event.level == helpers.LogLevelDebug {
			message += fmt.Sprintf("，任务=%d", event.sequence)
			if event.attempts != 0 {
				message += fmt.Sprintf("，尝试=%d", event.attempts)
			}
		}
		if event.account != 0 {
			message += fmt.Sprintf("，账号=%d", event.account)
		}
		source := event.result.StatusSource
		if source == "Emby" || source == "飞牛" || source == "网盘" {
			if code := event.result.HTTPStatus; code >= 100 && code <= 599 {
				message += fmt.Sprintf("，%s HTTP=%d", source, code)
			}
			if code := event.result.BusinessCode; code != 0 {
				message += fmt.Sprintf("，%s业务码=%d", source, code)
			}
		}
	}
	message += fmt.Sprintf("，耗时=%dms", event.elapsed.Milliseconds())
	if event.level == helpers.LogLevelWarn {
		if event.cooldown > 0 {
			message += fmt.Sprintf("；暂停该账号预加载至少%d分钟", (event.cooldown+time.Minute-1)/time.Minute)
		}
		message += "；播放时仍按正常流程取链"
	}
	return message
}

func (event preloadEvent) log() {
	if helpers.AppLogger == nil || helpers.ConfiguredLogLevel() > event.level {
		return
	}
	message := event.message()
	switch event.level {
	case helpers.LogLevelInfo:
		helpers.AppLogger.Info(message)
	case helpers.LogLevelWarn:
		helpers.AppLogger.Warn(message)
	default:
		helpers.AppLogger.Debug(message)
	}
}
