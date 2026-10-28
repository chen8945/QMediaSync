package main

import (
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/controllers"
	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/playback"
)

func TestPreloadOriginKey(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"http://QMS.invalid/path?token=private", "http://qms.invalid:80"},
		{"http://qms.invalid:80/other", "http://qms.invalid:80"},
		{"https://qms.invalid/", "https://qms.invalid:443"},
		{"http://[::1]:12333/115/url", "http://[::1]:12333"},
		{"http://user:password@qms.invalid/115/url", ""},
		{"file:///115/url", ""},
		{"//qms.invalid/115/url", ""},
	} {
		t.Run(test.raw, func(t *testing.T) {
			parsed, err := url.Parse(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if got := preloadOriginKey(parsed); got != test.want {
				t.Fatalf("来源规范化 = %q，期望 %q", got, test.want)
			}
		})
	}
	if preloadOriginKey(nil) != "" {
		t.Fatal("空来源不应获准")
	}
}

func TestPlaybackPreloadConfiguredPrefixes(t *testing.T) {
	const childEnv = "QMS_TEST_PRELOAD_CONFIGURED_PREFIXES"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPlaybackPreloadConfiguredPrefixes$", "-test.timeout=20s", "-test.v")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("已配置 STRM 前缀回归失败：%v\n%s", err, output)
		}
		return
	}
	setupFnosStartupTest(t)
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.SyncPath{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Db.Create(&models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "https://global.invalid/qms"}, StrmPreloadEnabled: 1}).Error; err != nil {
		t.Fatal(err)
	}
	for _, base := range []string{
		"https://global.invalid/qms/nested/", "http://directory.invalid:12333/media", "https://global.invalid/other",
		"https://invalid-query.invalid/qms?x=1", "https://invalid-fragment.invalid/qms#hidden",
		"https://invalid-encoded.invalid/%71ms", "https://invalid-traversal.invalid/qms/../other",
		"https://private@invalid-userinfo.invalid/qms",
	} {
		if err := db.Db.Create(&models.SyncPath{BaseCid: base, SettingStrm: models.SettingStrm{StrmBaseUrl: base}}).Error; err != nil {
			t.Fatal(err)
		}
	}
	models.LoadSettings()
	playback.DefaultPreloader = playback.NewPreloader()
	app := &App{}
	app.startPlaybackPreloading(t.Context())
	t.Cleanup(app.stopPlaybackPreloading)
	resolve, account := app.newEmbyPlaybackPreload, app.playbackPreloadAccount
	const query = "userid=User%2B001&pickcode=fixture-pick&force=0&display=a%2Bb"
	var queries atomic.Int32
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:preload_prefix_hot_reads", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Query().Remove("test:preload_prefix_hot_reads")
	for _, test := range []struct{ name, target, endpoint string }{
		{"global-newurl", "https://global.invalid/qms/115/newurl?" + query, "/115/newurl"},
		{"global-filename", "https://global.invalid/qms/115/url/movie.mkv?" + query, "/115/url/movie.mkv"},
		{"directory-baidu", "http://directory.invalid:12333/media/baidupan/url/movie.mkv?" + query, "/baidupan/url/movie.mkv"},
		{"global-baidu", "https://global.invalid/qms/baidupan/url/movie.mkv?" + query, "/baidupan/url/movie.mkv"},
		{"directory-115", "http://directory.invalid:12333/media/115/newurl?" + query, "/115/newurl"},
		{"overlapping-prefix", "https://global.invalid/qms/nested/115/newurl?" + query, "/115/newurl"},
		{"multiple-prefixes", "https://global.invalid/other/115/newurl?" + query, "/115/newurl"},
		{"canonical-origin", "https://GLOBAL.invalid:443/qms/115/newurl?" + query, "/115/newurl"},
		{"root-115", "https://global.invalid/115/newurl?" + query, "/115/newurl"},
		{"root-baidu", "http://directory.invalid:12333/baidupan/url/movie.mkv?" + query, "/baidupan/url/movie.mkv"},
		{"unknown-host", "https://unknown.invalid/qms/115/newurl?" + query, ""},
		{"unknown-root-host", "https://unknown.invalid/115/newurl?" + query, ""},
		{"invalid-config-query", "https://invalid-query.invalid/qms/115/newurl?" + query, ""},
		{"invalid-config-fragment", "https://invalid-fragment.invalid/qms/115/newurl?" + query, ""},
		{"invalid-config-encoded", "https://invalid-encoded.invalid/qms/115/newurl?" + query, ""},
		{"invalid-config-traversal", "https://invalid-traversal.invalid/other/115/newurl?" + query, ""},
		{"invalid-config-userinfo", "https://invalid-userinfo.invalid/qms/115/newurl?" + query, ""},
		{"wrong-scheme", "http://global.invalid/qms/115/newurl?" + query, ""},
		{"wrong-port", "https://global.invalid:444/qms/115/newurl?" + query, ""},
		{"similar-prefix", "https://global.invalid/qms2/115/newurl?" + query, ""},
		{"missing-boundary", "https://global.invalid/qms115/newurl?" + query, ""},
		{"unconfigured-prefix", "https://global.invalid/unknown/115/newurl?" + query, ""},
		{"prefix-at-other-origin", "http://directory.invalid:12333/qms/115/newurl?" + query, ""},
		{"userinfo", "https://private@global.invalid/qms/115/newurl?" + query, ""},
		{"fragment", "https://global.invalid/qms/115/newurl?" + query + "#hidden", ""},
		{"encoded-prefix", "https://global.invalid/%71ms/115/newurl?" + query, ""},
		{"encoded-endpoint", "https://global.invalid/qms/%31%31%35/newurl?" + query, ""},
		{"encoded-filename", "https://global.invalid/qms/115/url/%61.mkv?" + query, ""},
		{"encoded-traversal", "https://global.invalid/qms/%2e%2e/115/newurl?" + query, ""},
		{"dot-segment", "https://global.invalid/qms/./115/newurl?" + query, ""},
		{"traversal", "https://global.invalid/qms/../115/newurl?" + query, ""},
		{"duplicate-slash", "https://global.invalid/qms//115/newurl?" + query, ""},
		{"root-encoded", "https://global.invalid/%31%31%35/newurl?" + query, ""},
		{"backslash", "https://global.invalid/qms/115/url/a\\b.mkv?" + query, ""},
		{"encoded-null", "https://global.invalid/qms/115/url/a%00.mkv?" + query, ""},
		{"root-traversal", "https://global.invalid/115/url/../movie.mkv?" + query, ""},
		{"duplicate-pickcode", "https://global.invalid/qms/115/newurl?" + query + "&pickcode=other", ""},
		{"duplicate-userid", "https://global.invalid/qms/115/newurl?" + query + "&userid=other", ""},
		{"duplicate-force", "https://global.invalid/qms/115/newurl?" + query + "&force=1", ""},
		{"duplicate-extra", "https://global.invalid/qms/115/newurl?" + query + "&display=other", ""},
		{"invalid-query", "https://global.invalid/qms/115/newurl?" + query + "&x=%GG", ""},
		{"invalid-force", "https://global.invalid/qms/115/newurl?pickcode=fixture-pick&userid=User%2B001&force=x", ""},
		{"missing-pickcode", "https://global.invalid/qms/115/newurl?userid=user", ""},
		{"unsupported-openlist", "https://global.invalid/qms/openlist/url?" + query, ""},
		{"unsupported-endpoint", "https://global.invalid/qms/api/setting/strm-config?" + query, ""},
		{"empty-filename", "https://global.invalid/qms/115/url/?" + query, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			identity, run, ok := resolve(test.target, "Player/1.2", true)
			gotAccount := account(test.target)
			if test.endpoint == "" {
				if ok || run != nil || identity != "" || gotAccount != (playback.PreloadAccount{}) {
					t.Fatalf("不可信或无效来源被接纳：ok=%t account=%+v", ok, gotAccount)
				}
				return
			}
			expected, _, valid := newPlaybackPreload(test.endpoint+"?"+query, "Player/1.2", true)
			if !valid || !ok || run == nil || identity != expected {
				t.Errorf("已配置来源未按根端点准备：ok=%t identityMatch=%t", ok, identity == expected)
			}
			if expectedAccount := controllers.PlaybackPreloadAccount(test.endpoint + "?" + query); gotAccount != expectedAccount || gotAccount.UserID != "User+001" {
				t.Errorf("前缀来源丢失账号提示：got=%+v want=%+v", gotAccount, expectedAccount)
			}
			normalized, _ := app.normalizePreloadSource(test.target)
			parsed, err := url.Parse(normalized)
			if err != nil || parsed.Path != test.endpoint || parsed.RawQuery != query {
				t.Errorf("规范化改变了查询或端点：url=%q err=%v", normalized, err)
			}
		})
	}
	if queries.Load() != 0 {
		t.Fatalf("规范化和账号提示新增 SQL=%d", queries.Load())
	}
	current := app.preloadOrigins.Load()
	for _, state := range []string{"missing", "old-generation", "old-base", "failed-rebuild"} {
		t.Run(state, func(t *testing.T) {
			invalid := *current
			switch state {
			case "missing":
				app.preloadOrigins.Store(nil)
			case "old-generation":
				invalid.generation--
				app.preloadOrigins.Store(&invalid)
			case "old-base":
				invalid.baseURL = "https://obsolete.invalid/qms"
				app.preloadOrigins.Store(&invalid)
			case "failed-rebuild":
				invalid.origins = nil
				app.preloadOrigins.Store(&invalid)
			}
			defer app.preloadOrigins.Store(current)
			target, _ := url.Parse("https://global.invalid/qms/115/newurl?" + query)
			_, _, ok := resolve(target.String(), "Player/1.2", true)
			if ok || account(target.String()) != (playback.PreloadAccount{}) || app.isTrustedPreloadOrigin(target) {
				t.Fatal("旧代或重建失败的快照仍获准")
			}
		})
	}
	_, oldRun, ok := resolve("https://global.invalid/qms/115/newurl?"+query, "Player/1.2", true)
	if !ok {
		t.Fatal("来源更新前未解析测试回调")
	}
	before := models.GetPlaybackSettingsSnapshot()
	models.NotifyStrmOriginsChanged()
	if models.GetPlaybackSettingsSnapshot().Generation != before.Generation {
		t.Fatal("仅来源通知不应改变设置代际")
	}
	queries.Store(0)
	completed := make(chan playback.PreloadOutcome, 1)
	if !playback.DefaultPreloader.Submit(playback.PreloadCandidate{Key: "late-prepared-source", Scope: "late-prepared-source", Kind: playback.PreloadNext,
		Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			result := oldRun(ctx, task)
			completed <- result.Outcome
			return result
		}}) {
		t.Fatal("来源通知后的迟到提交未进入验证")
	}
	select {
	case outcome := <-completed:
		if outcome != playback.PreloadSkipped || queries.Load() != 0 {
			t.Fatalf("旧回调仍访问来源或签名：outcome=%d SQL=%d", outcome, queries.Load())
		}
	case <-time.After(time.Second):
		t.Fatal("迟到回调没有退出")
	}
}

func TestPlaybackPreloadLifecycle(t *testing.T) {
	// 设置快照属于进程生命周期，隔离进程避免污染其他应用测试。
	const childEnv = "QMS_TEST_PLAYBACK_PRELOAD_LIFECYCLE"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPlaybackPreloadLifecycle$", "-test.timeout=20s")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("预加载生命周期回归失败：%v\n%s", err, output)
		}
		return
	}
	setupFnosStartupTest(t)
	if err := db.Db.AutoMigrate(&models.Settings{}, &models.SyncPath{}); err != nil {
		t.Fatal(err)
	}
	settings := &models.Settings{SettingStrm: models.SettingStrm{StrmBaseUrl: "https://global.invalid/qms"}, StrmPreloadEnabled: 1}
	if err := db.Db.Create(settings).Error; err != nil {
		t.Fatal(err)
	}
	path := &models.SyncPath{BaseCid: "preload-test", SettingStrm: models.SettingStrm{StrmBaseUrl: "http://directory.invalid:12333/qms"}}
	if err := db.Db.Create(path).Error; err != nil {
		t.Fatal(err)
	}
	models.LoadSettings()
	playback.DefaultPreloader = playback.NewPreloader()
	app := &App{}
	app.startPlaybackPreloading(t.Context())
	t.Cleanup(app.stopPlaybackPreloading)
	assertTrusted := func(raw string, want bool) {
		t.Helper()
		target, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got := app.isTrustedPreloadOrigin(target); got != want {
			t.Fatalf("来源 %q 获准=%t，期望 %t", raw, got, want)
		}
	}
	assertPrepared := func(raw string, want bool) {
		t.Helper()
		_, run, ok := app.newEmbyPlaybackPreload(raw+"?pickcode=fixture-pick&userid=owner", "Player/1.2", true)
		if ok != want || (run != nil) != want {
			t.Fatalf("来源 %q 准备=%t，期望 %t", raw, ok, want)
		}
	}
	if !playback.DefaultPreloader.Enabled() {
		t.Fatal("已保存的启用状态未生效")
	}
	assertTrusted("https://global.invalid:443/115/url", true)
	assertTrusted("http://directory.invalid:12333/baidupan/url", true)
	assertTrusted("https://global.invalid.evil.test/115/url", false)
	assertTrusted("http://global.invalid/115/url", false)
	assertPrepared("https://global.invalid/qms/115/newurl", true)
	assertPrepared("http://directory.invalid:12333/qms/baidupan/url/movie.mkv", true)

	var queries atomic.Int32
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:preload_hot_reads", func(*gorm.DB) { queries.Add(1) }); err != nil {
		t.Fatal(err)
	}
	for range 100 {
		assertTrusted("https://global.invalid/115/url", true)
		assertPrepared("https://global.invalid/qms/115/newurl", true)
	}
	if queries.Load() != 0 {
		t.Fatal("来源判断不应在详情和播放心跳热路径查询数据库")
	}
	if err := db.Db.Callback().Query().Remove("test:preload_hot_reads"); err != nil {
		t.Fatal(err)
	}
	strm, multi := models.SettingsGlobal.StrmSnapshot()
	strm.StrmBaseUrl = "https://failed.invalid"
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:preload_failed_save", func(tx *gorm.DB) { tx.AddError(errors.New("test failed write")) }); err != nil {
		t.Fatal(err)
	}
	if models.SettingsGlobal.UpdateStrm(strm, multi) {
		t.Fatal("失败保存被接受")
	}
	assertTrusted("https://global.invalid/115/url", true)
	assertTrusted("https://failed.invalid/115/url", false)
	assertPrepared("https://global.invalid/qms/115/newurl", true)
	if err := db.Db.Callback().Update().Remove("test:preload_failed_save"); err != nil {
		t.Fatal(err)
	}
	strm.StrmBaseUrl = "https://updated.invalid"
	if !models.SettingsGlobal.UpdateStrm(strm, multi) {
		t.Fatal("来源更新失败")
	}
	assertTrusted("https://global.invalid/115/url", false)
	assertTrusted("https://updated.invalid/115/url", true)
	assertPrepared("https://global.invalid/qms/115/newurl", false)
	assertPrepared("https://updated.invalid/115/newurl", true)
	if err := db.Db.Model(path).Update("strm_base_url", "http://new-directory.invalid:12333").Error; err != nil {
		t.Fatal(err)
	}
	models.NotifyStrmOriginsChanged()
	assertTrusted("http://directory.invalid:12333/115/url", false)
	assertTrusted("http://new-directory.invalid:12333/115/url", true)
	assertPrepared("http://directory.invalid:12333/qms/115/newurl", false)
	assertPrepared("http://new-directory.invalid:12333/115/newurl", true)

	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:preload_failed_origins", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_paths" {
			tx.AddError(errors.New("test failed read"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	models.NotifyStrmOriginsChanged()
	assertTrusted("https://updated.invalid/115/url", false)
	assertPrepared("https://updated.invalid/115/newurl", false)
	if err := db.Db.Callback().Query().Remove("test:preload_failed_origins"); err != nil {
		t.Fatal(err)
	}
	models.NotifyStrmOriginsChanged()
	assertTrusted("https://updated.invalid/115/url", true)
	assertPrepared("https://updated.invalid/115/newurl", true)

	// 旧查询已完成读库但迟到返回时，不能覆盖新来源配置。
	blocked, unblock := make(chan struct{}), make(chan struct{})
	var once atomic.Bool
	if err := db.Db.Callback().Query().After("gorm:query").Register("test:preload_old_origins", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_paths" && once.CompareAndSwap(false, true) {
			close(blocked)
			<-unblock
		}
	}); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan struct{})
	go func() { models.NotifyStrmOriginsChanged(); close(oldDone) }()
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("未进入延迟来源查询")
	}
	strm.StrmBaseUrl = "https://latest.invalid"
	saved := models.SettingsGlobal.UpdateStrm(strm, multi)
	close(unblock)
	<-oldDone
	if !saved {
		t.Fatal("并发来源更新失败")
	}
	if err := db.Db.Callback().Query().Remove("test:preload_old_origins"); err != nil {
		t.Fatal(err)
	}
	assertTrusted("https://updated.invalid/115/url", false)
	assertTrusted("https://latest.invalid/115/url", true)
	assertPrepared("https://latest.invalid/115/newurl", true)
	strm.LocalProxy = 1
	if !models.SettingsGlobal.UpdateStrm(strm, multi) {
		t.Fatal("播放模式更新失败")
	}
	assertPrepared("https://latest.invalid/115/newurl", true)
	disabled := 0
	if !models.SettingsGlobal.UpdateStrm(strm, multi, &disabled) || playback.DefaultPreloader.Enabled() {
		t.Fatal("关闭设置未停止准入")
	}
	assertTrusted("https://latest.invalid/115/url", false)
	assertPrepared("https://latest.invalid/115/newurl", false)
	enabled := 1
	if !models.SettingsGlobal.UpdateStrm(strm, multi, &enabled) {
		t.Fatal("重新启用失败")
	}
	assertTrusted("https://latest.invalid/115/url", true)
	assertPrepared("https://latest.invalid/115/newurl", true)

	started := make(chan *playback.PreloadTask, 1)
	stopped := make(chan bool, 1)
	if !playback.DefaultPreloader.Submit(playback.PreloadCandidate{
		Key: "shutdown-source", Scope: "shutdown-device", Instance: "emby302", Kind: playback.PreloadNext,
		Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
			if !task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 1, UserID: "test-user"}, "shutdown-source") {
				return playback.PreloadResult{Outcome: playback.PreloadSkipped}
			}
			started <- task
			<-ctx.Done()
			stopped <- !task.Publish(func() {})
			return playback.PreloadResult{Outcome: playback.PreloadSkipped}
		},
	}) {
		t.Fatal("关闭测试作业未接纳")
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("关闭测试作业未启动")
	}
	adapterStopped := false
	app.stopEmbyPreload = func() { adapterStopped = true }
	app.stopPlaybackPreloading()
	if !adapterStopped || !<-stopped || playback.DefaultPreloader.Enabled() {
		t.Fatal("关闭未停止适配器、排空后台作业或阻止迟到发布")
	}
	assertTrusted("https://latest.invalid/115/url", false)
}

func TestPlaybackPreloadSourceChangesRevokeAllAdapters(t *testing.T) {
	const childEnv = "QMS_TEST_PRELOAD_SOURCE_CHANGES"
	if os.Getenv(childEnv) != "1" {
		command := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestPlaybackPreloadSourceChangesRevokeAllAdapters$", "-test.timeout=30s", "-test.v")
		command.Env = append(os.Environ(), childEnv+"=1")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("来源变更撤销回归失败：%v\n%s", err, output)
		}
		return
	}
	for _, change := range []string{"edit", "delete", "read-failure"} {
		for _, phase := range []string{"queued", "signing"} {
			t.Run(change+"/"+phase, func(t *testing.T) {
				setupFnosStartupTest(t)
				if err := db.Db.AutoMigrate(&models.Settings{}, &models.SyncPath{}); err != nil {
					t.Fatal(err)
				}
				if err := db.Db.Create(&models.Settings{StrmPreloadEnabled: 1}).Error; err != nil {
					t.Fatal(err)
				}
				path := &models.SyncPath{BaseCid: "source-change", SettingStrm: models.SettingStrm{StrmBaseUrl: "http://old.invalid"}}
				if err := db.Db.Create(path).Error; err != nil {
					t.Fatal(err)
				}
				models.LoadSettings()
				playback.DefaultPreloader = playback.NewPreloader()
				app := &App{}
				app.startPlaybackPreloading(t.Context())
				t.Cleanup(app.stopPlaybackPreloading)
				before := models.GetPlaybackSettingsSnapshot()
				started, completed := make(chan *playback.PreloadTask, 2), make(chan string, 2)
				release := make(chan struct{})
				for index, instance := range []string{"emby302", "fnos:source-change"} {
					kind := playback.PreloadNext
					if phase == "queued" {
						kind = playback.PreloadDetail
					}
					account := playback.PreloadAccount{Provider: "115", AccountID: uint(index + 1), UserID: instance}
					if !playback.DefaultPreloader.Submit(playback.PreloadCandidate{Key: instance, Scope: instance, Instance: instance, Kind: kind, Account: account,
						Run: func(ctx context.Context, task *playback.PreloadTask) playback.PreloadResult {
							if !task.BeginSignature(account, instance) {
								return playback.PreloadResult{Outcome: playback.PreloadSkipped}
							}
							started <- task
							select {
							case <-release:
							case <-ctx.Done():
							}
							if task.Publish(func() {}) {
								completed <- instance
							} else {
								completed <- ""
							}
							return playback.PreloadResult{Outcome: playback.PreloadPublished}
						}}) {
						t.Fatal("来源变更测试作业未接纳")
					}
				}
				if phase == "signing" {
					for range 2 {
						select {
						case task := <-started:
							if !task.Valid() {
								t.Fatal("来源提交前作业已失效")
							}
						case <-time.After(time.Second):
							t.Fatal("两个反代作业未进入签名")
						}
					}
				}
				if change == "delete" {
					if err := db.Db.Delete(path).Error; err != nil {
						t.Fatal(err)
					}
				} else if err := db.Db.Model(path).Update("strm_base_url", "http://new.invalid").Error; err != nil {
					t.Fatal(err)
				}
				if change == "read-failure" {
					if err := db.Db.Callback().Query().Before("gorm:query").Register("test:source_change_failed_read", func(tx *gorm.DB) {
						if tx.Statement.Table == "sync_paths" {
							tx.AddError(errors.New("test failed read"))
						}
					}); err != nil {
						t.Fatal(err)
					}
				}
				models.NotifyStrmOriginsChanged()
				close(release)
				if after := models.GetPlaybackSettingsSnapshot(); after.Generation != before.Generation || after.PreloadEnabled != before.PreloadEnabled {
					t.Fatal("目录来源通知不应伪造播放设置代际或切换开关")
				}
				if phase == "queued" {
					time.Sleep(2200 * time.Millisecond)
					if len(started) != 0 || len(completed) != 0 {
						t.Errorf("来源变更后仍执行旧排队作业：started=%d completed=%d", len(started), len(completed))
					}
				} else {
					for range 2 {
						select {
						case instance := <-completed:
							if instance != "" {
								t.Errorf("来源变更后旧签名仍发布：instance=%s", instance)
							}
						case <-time.After(time.Second):
							t.Fatal("来源变更后旧作业未退出")
						}
					}
				}
				fresh := make(chan struct{})
				if !playback.DefaultPreloader.Submit(playback.PreloadCandidate{Key: "fresh", Scope: "fresh", Instance: "fnos:source-change", Kind: playback.PreloadNext,
					Run: func(_ context.Context, task *playback.PreloadTask) playback.PreloadResult {
						if task.BeginSignature(playback.PreloadAccount{Provider: "115", AccountID: 99, UserID: "fresh-owner"}, "fresh") {
							task.Publish(func() { close(fresh) })
						}
						return playback.PreloadResult{Outcome: playback.PreloadPublished}
					}}) {
					t.Fatal("撤销旧作业后不应关闭新候选准入")
				}
				select {
				case <-fresh:
				case <-time.After(time.Second):
					t.Fatal("新候选未运行")
				}
			})
		}
	}
}
