package playback

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
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"qmediasync/internal/db"
)

// ExternalLink 是第三方 STRM 已完成解析的地址；期限不因读取缓存续期。
type ExternalLink struct {
	URL       string
	Status    int
	ExpiresAt time.Time
}

var externalLinkFlights singleflight.Group

var errExternalLeaderCanceled = fmt.Errorf("共享取链请求已取消: %w", context.Canceled)
var errExternalCacheExpired = errors.New("播放地址剩余缓存期限不足一秒")

// 只登记已经开始解析的 leader，供原本不主动取链的飞牛入口等待现有结果。
var activeExternalLinks sync.Map

// ValidExternalURL 不解释生成器参数，保留编码路径和原始查询串。
func ValidExternalURL(raw string) bool {
	if raw == "" || len(raw) > 16<<10 || strings.ContainsFunc(raw, func(r rune) bool { return r < ' ' || r == 127 }) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Hostname() != "" && u.User == nil && u.Opaque == "" && u.Fragment == ""
}

// NormalizeExternalURL 补齐路径和查询串的必要编码，保留原有转义及参数语义。
func NormalizeExternalURL(raw string) (string, bool) {
	if !ValidExternalURL(raw) {
		return "", false
	}
	u, _ := url.Parse(raw)
	u.RawPath = escapeExternalURLPath(u.RawPath)
	u.RawQuery = escapeExternalURLText(u.RawQuery)
	normalized := u.String()
	return normalized, len(normalized) <= 16<<10
}

var externalPathEscapes = strings.NewReplacer(
	"%25", "%", "%21", "!", "%27", "'", "%28", "(", "%29", ")",
	"%2A", "*", "%5B", "[", "%5D", "]",
)

func escapeExternalURLPath(raw string) string {
	encoded := (&url.URL{Path: raw}).EscapedPath()
	// RawPath 已通过 url.Parse 校验；单轮还原 validEncoded 允许的字面字符，不能再次解码原 %HH。
	return externalPathEscapes.Replace(encoded)
}

func escapeExternalURLText(raw string) string {
	const hex = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == ' ' || c >= 128 {
			encoded.WriteByte('%')
			encoded.WriteByte(hex[c>>4])
			encoded.WriteByte(hex[c&15])
		} else {
			encoded.WriteByte(c)
		}
	}
	return encoded.String()
}

// ExternalPreloadAccount 以来源 origin 限制后台请求，不伪造本地网盘账号。
func ExternalPreloadAccount(raw string) PreloadAccount {
	if !ValidExternalURL(raw) {
		return PreloadAccount{}
	}
	u, _ := url.Parse(raw)
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	} else {
		port = strings.TrimLeft(port, "0")
		if port == "" {
			port = "0"
		}
	}
	return PreloadAccount{Provider: "http", UserID: u.Scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)}
}

// ExternalLinkIdentity 同时用于后台候选、正常播放接手和共享缓存。
func ExternalLinkIdentity(raw, ua string) string {
	if normalized, ok := NormalizeExternalURL(raw); ok {
		raw = normalized
	}
	encoded, _ := json.Marshal([]string{raw, ua})
	return fmt.Sprintf("external-link:%x", sha256.Sum256(encoded))
}

// CachedExternalLink 只返回同一规范化源地址与完整 UA 的有效结果。
func CachedExternalLink(raw, ua string) (ExternalLink, bool) {
	var link ExternalLink
	if db.Cache.CacheInstance == nil || json.Unmarshal(db.Cache.Get(ExternalLinkIdentity(raw, ua)), &link) != nil ||
		!link.ExpiresAt.After(time.Now()) || !ValidExternalURL(link.URL) || (link.Status != http.StatusOK && link.Status != http.StatusPartialContent) {
		return ExternalLink{}, false
	}
	return link, true
}

// WaitExternalLink 只等待已有解析，miss 不发起网络请求。
func WaitExternalLink(ctx context.Context, raw, ua string) (ExternalLink, bool) {
	if link, ok := CachedExternalLink(raw, ua); ok {
		return link, true
	}
	done, active := activeExternalLinks.Load(ExternalLinkIdentity(raw, ua))
	if !active {
		return CachedExternalLink(raw, ua)
	}
	ctx, cancel := context.WithTimeout(ctx, preloadRunLimit)
	defer cancel()
	select {
	case <-ctx.Done():
		return ExternalLink{}, false
	case <-done.(chan struct{}):
		return CachedExternalLink(raw, ua)
	}
}

func cacheExternalLink(raw, ua string, link ExternalLink) error {
	if db.Cache.CacheInstance == nil {
		return db.ErrCacheUnavailable
	}
	encoded, err := json.Marshal(link)
	if err != nil {
		return err
	}
	seconds := int(time.Until(link.ExpiresAt) / time.Second)
	if seconds <= 0 {
		return errExternalCacheExpired
	}
	return db.Cache.Set(ExternalLinkIdentity(raw, ua), encoded, seconds)
}

// NewExternalPreload 复用有界协调器与正式播放的解析结果，不在构造时发请求。
func NewExternalPreload(raw, ua string, transport http.RoundTripper) (string, func(context.Context, *PreloadTask) PreloadResult, bool) {
	raw, ok := NormalizeExternalURL(raw)
	if !ok || strings.TrimSpace(ua) == "" || len(ua) > 4096 || strings.ContainsFunc(ua, func(r rune) bool { return r < ' ' || r == 127 }) {
		return "", nil, false
	}
	identity, account := ExternalLinkIdentity(raw, ua), ExternalPreloadAccount(raw)
	run := func(ctx context.Context, task *PreloadTask) PreloadResult {
		if ctx.Err() != nil || !task.Valid() {
			return PreloadResult{Outcome: PreloadSkipped, Reason: "canceled"}
		}
		if db.Cache.CacheInstance == nil {
			return PreloadResult{Outcome: PreloadSkipped, Reason: "cache_unavailable"}
		}
		if !task.BindSource(account, identity) {
			return PreloadResult{Outcome: PreloadSkipped, Reason: "source_unavailable"}
		}
		if _, ok := CachedExternalLink(raw, ua); ok {
			return PreloadResult{Outcome: PreloadCached}
		}
		if !task.beginSource(account, identity) {
			return PreloadResult{Outcome: PreloadSkipped, Reason: "external_unavailable"}
		}
		headers := http.Header{"User-Agent": {ua}, "Range": {"bytes=0-0"}}
		var published, attempted bool
		failure := PreloadResult{Outcome: PreloadSkipped, Reason: "canceled"}
		publish := func(link ExternalLink) {
			attempted = true
			published = task.Publish(func() bool {
				err := cacheExternalLink(raw, ua, link)
				if err == nil {
					return true
				}
				failure = PreloadResult{Outcome: PreloadFailed, Reason: "cache_write_failed"}
				switch {
				case errors.Is(err, db.ErrCacheUnavailable):
					failure.Reason = "cache_unavailable"
				case errors.Is(err, errExternalCacheExpired):
					failure.Reason = "cache_expired"
				}
				return false
			})
		}
		link, err := externalLink(ctx, raw, headers, transport, publish)
		if ctx.Err() != nil || !task.Valid() {
			return PreloadResult{Outcome: PreloadSkipped, Reason: "canceled"}
		}
		if err != nil {
			return externalPreloadFailure(err)
		}
		// leader 已尝试发布时不重试缓存；等待者仍需通过自己的发布门。
		if !attempted {
			publish(link)
		}
		if !published {
			return failure
		}
		return PreloadResult{Outcome: PreloadPublished}
	}
	return identity, run, true
}

// ResolveExternalLink 让正式播放接手相同资源，缓存 miss 才按需解析。
func ResolveExternalLink(ctx context.Context, raw string, headers http.Header, transport http.RoundTripper) (ExternalLink, error) {
	ua := headers.Get("User-Agent")
	DefaultPreloader.Takeover(ExternalLinkIdentity(raw, ua))
	if link, ok := CachedExternalLink(raw, ua); ok {
		return link, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	publish := func(link ExternalLink) { cacheExternalLink(raw, ua, link) }
	for {
		link, err := externalLink(ctx, raw, headers, transport, publish)
		if ctx.Err() != nil {
			return ExternalLink{}, ctx.Err()
		}
		// 首个正式请求取消后，仍有效的等待者在原期限内重新共享取链。
		if errors.Is(err, errExternalLeaderCanceled) {
			continue
		}
		if err == nil {
			publish(link)
		}
		return link, err
	}
}

func externalLink(ctx context.Context, raw string, headers http.Header, transport http.RoundTripper, publish func(ExternalLink)) (ExternalLink, error) {
	if ctx.Err() != nil {
		return ExternalLink{}, ctx.Err()
	}
	ua := headers.Get("User-Agent")
	headers = http.Header{"User-Agent": {ua}, "Range": {headers.Get("Range")}}
	identity := ExternalLinkIdentity(raw, ua)
	result := externalLinkFlights.DoChan(identity, func() (any, error) {
		done := make(chan struct{})
		activeExternalLinks.Store(identity, done)
		defer func() {
			close(done)
			activeExternalLinks.Delete(identity)
		}()
		if link, ok := CachedExternalLink(raw, ua); ok {
			return link, nil
		}
		link, err := resolveExternalLink(ctx, raw, headers, transport)
		if errors.Is(ctx.Err(), context.Canceled) {
			return ExternalLink{}, errExternalLeaderCanceled
		}
		if err == nil && ctx.Err() == nil {
			publish(link)
		}
		return link, err
	})
	select {
	case <-ctx.Done():
		return ExternalLink{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return ExternalLink{}, value.Err
		}
		return value.Val.(ExternalLink), nil
	}
}

type externalStatusError struct {
	status     int
	retryAfter time.Duration
}

func (e externalStatusError) Error() string {
	return fmt.Sprintf("第三方取链 HTTP 状态不符：%d", e.status)
}

func resolveExternalLink(ctx context.Context, raw string, headers http.Header, transport http.RoundTripper) (ExternalLink, error) {
	raw, ok := NormalizeExternalURL(raw)
	if !ok {
		return ExternalLink{}, errors.New("第三方取链地址无效")
	}
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for redirects := 0; ; redirects++ {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return ExternalLink{}, err
		}
		// 媒体服务器凭据不属于 STRM 服务；Range 不参与签名缓存身份。
		request.Header.Set("User-Agent", headers.Get("User-Agent"))
		request.Header.Set("Range", headers.Get("Range"))
		if request.Header.Get("Range") == "" {
			request.Header.Set("Range", "bytes=0-0")
		}
		request.Header.Set("Accept-Encoding", "identity")
		response, err := client.Do(request)
		if err != nil {
			return ExternalLink{}, err
		}
		// 只确认跳转链终点；服务忽略 Range 时也不读取完整响应。
		response.Body.Close()
		switch response.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			location := response.Header.Get("Location")
			if location == "" || redirects >= 10 {
				return ExternalLink{}, errors.New("第三方跳转缺少地址或次数过多")
			}
			target, err := url.Parse(location)
			if err != nil {
				return ExternalLink{}, errors.New("第三方跳转地址无效")
			}
			target.RawPath = escapeExternalURLPath(target.RawPath)
			target.RawQuery = escapeExternalURLText(target.RawQuery)
			target = request.URL.ResolveReference(target)
			raw, ok = NormalizeExternalURL(target.String())
			if !ok {
				return ExternalLink{}, errors.New("第三方跳转地址无效")
			}
			continue
		case http.StatusOK, http.StatusPartialContent:
			contentType := strings.TrimSpace(strings.ToLower(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0]))
			if contentType == "text/html" || contentType == "application/xhtml+xml" || contentType == "application/json" || strings.HasSuffix(contentType, "+json") {
				return ExternalLink{}, errors.New("第三方取链返回网页或 JSON")
			}
			return ExternalLink{URL: raw, Status: response.StatusCode, ExpiresAt: externalLinkExpiresAt(raw, time.Now())}, nil
		default:
			retryAfter := time.Duration(0)
			if seconds, err := strconv.ParseInt(response.Header.Get("Retry-After"), 10, 32); err == nil && seconds > 0 {
				retryAfter = time.Duration(seconds) * time.Second
			} else if deadline, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
				retryAfter = max(0, time.Until(deadline))
			}
			return ExternalLink{}, externalStatusError{status: response.StatusCode, retryAfter: retryAfter}
		}
	}
}

func externalLinkExpiresAt(raw string, now time.Time) time.Time {
	expiresAt := now.Add(10 * time.Minute)
	u, _ := url.Parse(raw)
	host := strings.ToLower(u.Hostname())
	if host != "115cdn.net" && !strings.HasSuffix(host, ".115cdn.net") {
		return expiresAt
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["t"]) != 1 {
		return time.Time{}
	}
	if deadline, err := strconv.ParseInt(query.Get("t"), 10, 64); err != nil || deadline <= 0 {
		return time.Time{}
	}
	expiresAt = minTime(expiresAt, URLExpiresAt(raw, now))
	if !expiresAt.After(now) {
		return time.Time{}
	}
	return expiresAt
}

func externalPreloadFailure(err error) PreloadResult {
	result := PreloadResult{Outcome: PreloadFailed, Reason: "external_failed", StatusSource: "第三方"}
	if status, ok := errors.AsType[externalStatusError](err); ok {
		result.HTTPStatus, result.RetryAfter = status.status, status.retryAfter
		switch status.status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusTooManyRequests:
			result.Outcome, result.Reason = PreloadRisk, "external_limited"
		case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			result.Outcome = PreloadTransient
		}
	} else if _, ok := errors.AsType[*url.Error](err); ok && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		result.Outcome = PreloadTransient
	}
	return result
}
