package controllers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/baidupan"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
	"qmediasync/internal/requests"
	"qmediasync/internal/v115open"
	"qmediasync/internal/validation"
)

// PlaybackPreloadSource 只接受适配器已验证来源、当前用户权限的稳定内部 STRM 地址。
// URL 的主机不会被访问；ForceDirect 对齐 Emby 自有取链接口的 force=1 语义。
type PlaybackPreloadSource struct {
	URL         string
	UA          string
	ForceDirect bool
}

type preparedPlaybackPreload struct {
	account   *models.Account
	source    playback.SourceKey
	slot      playback.Slot
	cacheKey  string
	identity  string
	sign115   func(context.Context) (*v115open.DownloadUrlResult, error)
	signBaidu func(context.Context) (string, error)
}

// PlaybackPreloadAccount 从已授权的稳定内部地址提取账号提示，不执行 I/O 或代替授权校验。
func PlaybackPreloadAccount(rawURL string) playback.PreloadAccount {
	provider, request, ok := parsePlaybackPreloadSource(rawURL)
	if !ok || strings.TrimSpace(request.UserID) == "" {
		return playback.PreloadAccount{}
	}
	return playback.PreloadAccount{Provider: provider, UserID: request.UserID}
}

func parsePlaybackPreloadSource(rawURL string) (string, requests.RemoteFileURLRequest, bool) {
	invalid := requests.RemoteFileURLRequest{}
	u, err := url.Parse(rawURL)
	if err != nil || u.User != nil || u.Fragment != "" || (u.Scheme != "" && u.Scheme != "http" && u.Scheme != "https") {
		return "", invalid, false
	}
	provider := ""
	switch {
	case u.Path == "/115/newurl", strings.HasPrefix(u.Path, "/115/url/") && len(u.Path) > len("/115/url/"):
		provider = string(models.SourceType115)
	case strings.HasPrefix(u.Path, "/baidupan/url/") && len(u.Path) > len("/baidupan/url/"):
		provider = string(models.SourceTypeBaiduPan)
	default:
		return "", invalid, false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["pickcode"]) != 1 || len(query["userid"]) > 1 || len(query["force"]) > 1 {
		return "", invalid, false
	}
	force := 0
	if value := query.Get("force"); value != "" {
		force, err = strconv.Atoi(value)
		if err != nil {
			return "", invalid, false
		}
	}
	request := requests.RemoteFileURLRequest{PickCode: query.Get("pickcode"), UserID: query.Get("userid"), Force: force}
	if request.Validate() != nil || len(request.PickCode) > 256 || len(request.UserID) > 256 {
		return "", invalid, false
	}
	return provider, request, true
}

// NewPlaybackPreload 不执行 I/O；回调仅由协调器调用并保留本轮已核验资料。
func NewPlaybackPreload(input PlaybackPreloadSource) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
	provider, request, ok := parsePlaybackPreloadSource(input.URL)
	if !ok || len(input.UA) > 4096 || strings.ContainsAny(input.UA, "\r\n") {
		return "", nil, false
	}
	if input.ForceDirect {
		request.Force = 1
	}
	settings := models.GetPlaybackSettingsSnapshot()
	mode, effectiveUA := playback.ModeProxy, input.UA
	cacheKey := baiduURLCacheKey(request.PickCode, input.UA)
	if provider == string(models.SourceType115) {
		mode = v115URLPlaybackMode(request.Force, settings.LocalProxy)
		effectiveUA = v115EffectiveUA(request.Force, settings.LocalProxy, input.UA)
		cacheKey = v115URLCacheKey(request.PickCode, request.Force, settings.LocalProxy, input.UA)
	}
	if provider == string(models.SourceType115) && mode == playback.ModeDirect && strings.TrimSpace(effectiveUA) == "" {
		return "", nil, false
	}
	identity := playbackPreloadIdentity(provider, cacheKey)
	var prepared *preparedPlaybackPreload
	run := func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
		if !task.Valid() || ctx.Err() != nil || models.GetPlaybackSettingsSnapshot().Generation != settings.Generation {
			return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "canceled"}
		}
		if prepared == nil {
			if !task.BindSource(playback.PreloadAccount{Provider: provider, UserID: request.UserID}, identity) {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_unavailable"}
			}
			account := &models.Account{}
			lookup := db.Db.WithContext(ctx).Where("source_type = ?", provider)
			if request.UserID != "" {
				lookup = lookup.Where("user_id = ?", request.UserID)
			} else {
				var file models.SyncFile
				if db.Db.WithContext(ctx).Where("source_type = ? AND pick_code = ?", provider, request.PickCode).First(&file).Error != nil {
					return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "account_lookup_failed"}
				}
				lookup = lookup.Where("id = ?", file.AccountId)
			}
			if lookup.First(account).Error != nil {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "account_lookup_failed"}
			}
			if account.Token == "" || account.TokenFailedReason != "" || account.UserId == "" ||
				account.TokenExpiriesTime > 0 && account.TokenExpiriesTime <= time.Now().Unix() {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "account_unavailable"}
			}
			bound := playback.PreloadAccount{Provider: provider, AccountID: account.ID, UserID: account.UserId}
			if !task.BindSource(bound, identity) {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_unavailable"}
			}
			prepared = &preparedPlaybackPreload{
				account: account, source: playback.SourceKey{AccountID: account.ID, UserID: account.UserId, PickCode: request.PickCode},
				slot: playback.Slot{Mode: mode, UA: effectiveUA}, cacheKey: cacheKey, identity: identity,
			}
			if provider == string(models.SourceType115) {
				client := v115open.NewPlaybackClient(account.ID, account.AppId, account.Token, "")
				prepared.sign115 = func(ctx context.Context) (*v115open.DownloadUrlResult, error) {
					return client.GetDownloadURLWithError(v115open.WithPreloadOperation(ctx), request.PickCode, effectiveUA, false)
				}
			} else {
				client := baidupan.NewBaiDuPanClientWithToken(account.Token)
				prepared.signBaidu = func(ctx context.Context) (string, error) {
					return client.GetPreloadDownloadURL(ctx, request.PickCode)
				}
			}
		}
		return warmPlaybackPreload(ctx, task, prepared)
	}
	return identity, run, true
}

func baiduURLCacheKey(pickCode, ua string) string {
	return fmt.Sprintf("baidupanurl:%s, ua=%s", pickCode, ua)
}

func playbackPreloadIdentity(provider, cacheKey string) string {
	value, _ := json.Marshal([]string{provider, cacheKey})
	return fmt.Sprintf("%x", sha256.Sum256(value))
}

func warmPlaybackPreload(ctx context.Context, task *playback.PreloadTask, prepared *preparedPlaybackPreload) playback.PreloadResult {
	skipped := playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "canceled"}
	if ctx.Err() != nil || !task.Valid() {
		return skipped
	}
	if prepared.account.TokenExpiriesTime > 0 && prepared.account.TokenExpiriesTime <= time.Now().Unix() {
		return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "account_unavailable"}
	}
	if !keyLock.TryAcquire(prepared.cacheKey) {
		return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_busy"}
	}
	defer keyLock.Unlock(prepared.cacheKey)
	if string(db.Cache.Get(prepared.cacheKey)) != "" {
		return playback.PreloadResult{Outcome: playback.PreloadCached}
	}
	reserved := prepared.sign115 != nil
	finished := false
	if reserved {
		other := v115Playback.Begin(prepared.source, prepared.slot, time.Now())
		defer func() {
			if !finished {
				v115Playback.Finish(prepared.source, prepared.slot, time.Time{})
			}
		}()
		if other {
			return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "source_busy"}
		}
	}
	account := playback.PreloadAccount{Provider: string(prepared.account.SourceType), AccountID: prepared.account.ID, UserID: prepared.account.UserId}
	if !task.BeginSignature(account, prepared.identity) || ctx.Err() != nil {
		return playback.PreloadResult{Outcome: playback.PreloadSkipped, Reason: "signature_unavailable"}
	}
	var value string
	var err error
	if reserved {
		var result *v115open.DownloadUrlResult
		result, err = prepared.sign115(ctx)
		if result != nil {
			value = result.URL
		}
	} else {
		value, err = prepared.signBaidu(ctx)
	}
	if ctx.Err() != nil || !task.Valid() {
		return skipped
	}
	if err != nil {
		return playbackPreloadError(err)
	}
	if validation.DownloadProxyURL("url", value) != nil {
		return playback.PreloadResult{Outcome: playback.PreloadFailed, Reason: "invalid_link"}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil || parsed.Fragment != "" {
		return playback.PreloadResult{Outcome: playback.PreloadFailed, Reason: "invalid_link"}
	}
	now := time.Now()
	deadline := now.Add(27000 * time.Second)
	if reserved {
		deadline = playback.URLExpiresAt(value, now)
	}
	ttl := int(deadline.Unix() - now.Unix())
	if ttl <= 0 {
		return playback.PreloadResult{Outcome: playback.PreloadFailed, Reason: "invalid_link"}
	}
	if !task.Publish(func() {
		db.Cache.Set(prepared.cacheKey, []byte(value), ttl)
		if reserved {
			v115Playback.Finish(prepared.source, prepared.slot, time.Unix(deadline.Unix(), 0))
			finished = true
		}
	}) {
		return skipped
	}
	return playback.PreloadResult{Outcome: playback.PreloadPublished}
}

func playbackPreloadError(err error) playback.PreloadResult {
	result := playback.PreloadResult{Outcome: playback.PreloadFailed, Reason: "provider_failed", StatusSource: "网盘"}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		result.Outcome = playback.PreloadSkipped
		result.Reason = "canceled"
		if errors.Is(err, context.DeadlineExceeded) {
			result.Reason = "timeout"
		}
		return result
	}
	if apiErr, ok := errors.AsType[*v115open.OpenAPIError](err); ok {
		result.HTTPStatus, result.BusinessCode, result.RetryAfter = apiErr.HTTPStatus, apiErr.Code, apiErr.RetryAfter
		switch {
		case v115open.IsRateLimited(err):
			result.Outcome = playback.PreloadRisk
		case apiErr.HTTPStatus == http.StatusUnauthorized || apiErr.HTTPStatus == http.StatusForbidden ||
			apiErr.Code == v115open.ACCESS_TOKEN_AUTH_FAIL || apiErr.Code == v115open.ACCESS_AUTH_INVALID || apiErr.Code == v115open.ACCESS_TOKEN_EXPIRY_CODE:
			result.Outcome = playback.PreloadUnauthorized
		case apiErr.Code == 0 && (apiErr.HTTPStatus == 502 || apiErr.HTTPStatus == 503 || apiErr.HTTPStatus == 504):
			result.Outcome = playback.PreloadTransient
		}
		return result
	}
	if apiErr, ok := errors.AsType[*baidupan.PreloadDownloadError](err); ok {
		result.HTTPStatus, result.BusinessCode, result.RetryAfter = apiErr.HTTPStatus, apiErr.Code, apiErr.RetryAfter
		switch {
		case apiErr.Risk:
			result.Outcome = playback.PreloadRisk
		case apiErr.Unauthorized:
			result.Outcome = playback.PreloadUnauthorized
		case apiErr.Transient:
			result.Outcome = playback.PreloadTransient
		}
		return result
	}
	if dns, ok := errors.AsType[*net.DNSError](err); ok && dns.IsNotFound {
		return result
	}
	if opErr, ok := errors.AsType[*net.OpError](err); ok {
		result.Outcome = playback.PreloadTransient
		result.Reason = "connection_failed"
		if opErr.Timeout() {
			result.Reason = "timeout_provider"
		}
	}
	return result
}
