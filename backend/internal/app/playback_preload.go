package app

import (
	"context"
	"net"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
)

type trustedPreloadOrigins struct {
	baseURL    string
	generation uint64
	origins    map[string][]string
}

func newPlaybackPreload(rawURL, ua string, forceDirect bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
	return controllers.NewPlaybackPreload(controllers.PlaybackPreloadSource{URL: rawURL, UA: ua, ForceDirect: forceDirect})
}

func (app *App) newEmbyPlaybackPreload(rawURL, ua string, forceDirect bool) (string, func(context.Context, *playback.PreloadTask) playback.PreloadResult, bool) {
	normalized, origins := app.normalizePreloadSource(rawURL)
	if origins == nil {
		return "", nil, false
	}
	identity, run, ok := newPlaybackPreload(normalized, ua, forceDirect)
	if !ok {
		return "", nil, false
	}
	return identity, func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
		// 来源通知与提交交错时，也不能让撤销前解析的地址启动新作业。
		if app.preloadOrigins.Load() != origins || models.GetPlaybackSettingsSnapshot().Generation != origins.generation {
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		}
		return run(ctx, task)
	}, true
}

func (app *App) playbackPreloadAccount(rawURL string) playback.PreloadAccount {
	normalized, origins := app.normalizePreloadSource(rawURL)
	if origins == nil {
		return playback.PreloadAccount{}
	}
	return controllers.PlaybackPreloadAccount(normalized)
}

func (app *App) normalizePreloadSource(rawURL string) (string, *trustedPreloadOrigins) {
	target, err := url.Parse(rawURL)
	if err != nil || target.Opaque != "" || target.Fragment != "" || target.RawPath != "" ||
		path.Clean(target.Path) != target.Path || strings.ContainsAny(target.Path, "\\\x00") {
		return "", nil
	}
	current := app.preloadOrigins.Load()
	settings := models.GetPlaybackSettingsSnapshot()
	if current == nil || !settings.PreloadEnabled || current.generation != settings.Generation || current.baseURL != settings.StrmBaseURL {
		return "", nil
	}
	prefixes, trusted := current.origins[preloadOriginKey(target)]
	if !trusted {
		return "", nil
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return "", nil
	}
	for _, values := range query {
		if len(values) != 1 {
			return "", nil
		}
	}
	if !preloadEndpoint(target.Path) {
		endpoint, longest := "", -1
		for _, prefix := range prefixes {
			if candidate, matched := strings.CutPrefix(target.Path, prefix); matched && len(prefix) > longest && preloadEndpoint(candidate) {
				endpoint, longest = candidate, len(prefix)
			}
		}
		if endpoint == "" {
			return "", nil
		}
		target.Path = endpoint
	}
	return target.String(), current
}

func preloadEndpoint(endpoint string) bool {
	return endpoint == "/115/newurl" ||
		strings.HasPrefix(endpoint, "/115/url/") && len(endpoint) > len("/115/url/") ||
		strings.HasPrefix(endpoint, "/baidupan/url/") && len(endpoint) > len("/baidupan/url/")
}

func (app *App) startPlaybackPreloading(ctx context.Context) {
	ctx, app.preloadCancel = context.WithCancel(ctx)
	playback.DefaultPreloader.Start(ctx)
	models.SetPlaybackSettingsChangeHook(func(models.PlaybackSettingsSnapshot) {
		// 通知可能交错，始终以最新一次成功发布的完整设置为准。
		settings := models.GetPlaybackSettingsSnapshot()
		playback.DefaultPreloader.Apply(settings.PreloadEnabled, settings.Generation)
		origins := app.preloadOrigins.Load()
		if origins == nil || origins.generation != settings.Generation || origins.baseURL != settings.StrmBaseURL || (settings.PreloadEnabled && origins.origins == nil) || (!settings.PreloadEnabled && origins.origins != nil) {
			app.refreshPreloadOrigins(ctx)
		}
	})
	models.SetStrmOriginsChangeHook(func() { app.refreshPreloadOrigins(ctx) })
	app.refreshPreloadOrigins(ctx)
	settings := models.GetPlaybackSettingsSnapshot()
	playback.DefaultPreloader.Apply(settings.PreloadEnabled, settings.Generation)
}

func (app *App) stopPlaybackPreloading() {
	models.SetPlaybackSettingsChangeHook(nil)
	models.SetStrmOriginsChangeHook(nil)
	app.preloadOrigins.Store(nil)
	if app.preloadCancel != nil {
		app.preloadCancel()
	}
	if app.stopEmbyPreload != nil {
		app.stopEmbyPreload()
		app.stopEmbyPreload = nil
	}
	playback.DefaultPreloader.Stop()
}

// 来源列表只在配置提交时重建；先关闭旧准入，再用 CAS 防止迟到的查询覆盖新配置。
func (app *App) refreshPreloadOrigins(ctx context.Context) {
	settings := models.GetPlaybackSettingsSnapshot()
	invalid := &trustedPreloadOrigins{baseURL: settings.StrmBaseURL, generation: settings.Generation}
	for {
		previous := app.preloadOrigins.Load()
		if previous != nil && previous.generation > settings.Generation {
			return
		}
		if app.preloadOrigins.CompareAndSwap(previous, invalid) {
			break
		}
	}
	playback.DefaultPreloader.CancelAll()
	if !settings.PreloadEnabled || db.Db == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var paths []models.SyncPath
	if err := db.Db.WithContext(ctx).Select("strm_base_url").Find(&paths).Error; err != nil {
		if ctx.Err() == nil && helpers.AppLogger != nil {
			helpers.AppLogger.Warnf("暂时无法读取 STRM 来源配置，已跳过 Emby 预加载，播放仍按正常流程处理")
		}
		return
	}
	origins := make(map[string][]string, len(paths)+1)
	add := func(raw string) {
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Opaque != "" || parsed.Fragment != "" || parsed.RawQuery != "" || parsed.RawPath != "" {
			return
		}
		prefix := strings.TrimRight(parsed.Path, "/")
		if prefix != "" && path.Clean(prefix) != prefix || strings.ContainsAny(prefix, "\\\x00") {
			return
		}
		if key := preloadOriginKey(parsed); key != "" {
			if !slices.Contains(origins[key], prefix) {
				origins[key] = append(origins[key], prefix)
			}
		}
	}
	add(settings.StrmBaseURL)
	for _, path := range paths {
		add(path.StrmBaseUrl)
	}
	if ctx.Err() == nil {
		app.preloadOrigins.CompareAndSwap(invalid, &trustedPreloadOrigins{baseURL: settings.StrmBaseURL, generation: settings.Generation, origins: origins})
	}
}

func (app *App) isTrustedPreloadOrigin(target *url.URL) bool {
	current := app.preloadOrigins.Load()
	settings := models.GetPlaybackSettingsSnapshot()
	if current == nil || !settings.PreloadEnabled || current.generation != settings.Generation || current.baseURL != settings.StrmBaseURL {
		return false
	}
	_, found := current.origins[preloadOriginKey(target)]
	return found
}

func preloadOriginKey(target *url.URL) string {
	if target == nil || target.User != nil || (target.Scheme != "http" && target.Scheme != "https") || target.Hostname() == "" {
		return ""
	}
	port := target.Port()
	if port == "" {
		port = "80"
		if target.Scheme == "https" {
			port = "443"
		}
	}
	return target.Scheme + "://" + net.JoinHostPort(strings.ToLower(target.Hostname()), port)
}
