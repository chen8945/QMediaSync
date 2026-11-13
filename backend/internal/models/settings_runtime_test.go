package models

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/openai"
	"qmediasync/internal/tmdb"
)

func TestSettingsSavesApplyToNewHTTPScopesAndPreserveCapturedClients(t *testing.T) {
	setupHttpProxyTest(t)
	type received struct {
		url, key, language, auth string
		body                     openai.ChatCompletionRequest
	}
	requests := make(chan received, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := received{url: "http://" + r.Host + r.URL.Path, key: r.URL.Query().Get("api_key"), language: r.URL.Query().Get("language"), auth: r.Header.Get("Authorization")}
		if r.Method == http.MethodPost {
			if err := json.NewDecoder(r.Body).Decode(&got.body); err != nil {
				t.Error(err)
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
		}
		requests <- got
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"movie","choices":[{"message":{"content":"{\"name\":\"movie\",\"year\":2024}"}}]}`))
	})
	first, second := httptest.NewServer(handler), httptest.NewServer(handler)
	defer first.Close()
	defer second.Close()
	ctx := context.Background()
	settings := GetScrapeSettingsSnapshot()
	settings.TmdbEnableProxy = false
	settings.TmdbUrl, settings.TmdbApiKey, settings.TmdbLanguage = first.URL, "old-tmdb", "zh-CN"
	settings.AiBaseUrl, settings.AiApiKey, settings.AiModelName = first.URL, "old-ai", "old-model"
	if err := SaveTmdbSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := SaveAISettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	type scope struct {
		settings ScrapeSettings
		tmdb     *tmdb.Client
		ai       *openai.Client
	}
	oldSettings := GetScrapeSettingsSnapshot()
	oldClient, err := oldSettings.GetTmdbClient()
	if err != nil {
		t.Fatal(err)
	}
	defer oldClient.Close()
	oldAI := oldSettings.GetAiClient()
	defer oldAI.Close()
	scopes := []scope{{oldSettings, oldClient, oldAI}}
	settings.TmdbUrl, settings.TmdbApiKey, settings.TmdbLanguage = second.URL, "new-tmdb", "en-US"
	settings.AiBaseUrl, settings.AiApiKey, settings.AiModelName = second.URL, "new-ai", "new-model"
	if err := SaveTmdbSettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	if err := SaveAISettings(ctx, settings); err != nil {
		t.Fatal(err)
	}
	current := GetScrapeSettingsSnapshot()
	draft := current
	draft.TmdbApiKey, draft.TmdbLanguage = "draft-tmdb", "ja-JP"
	draft.AiApiKey, draft.AiModelName = "draft-ai", "draft-model"
	for _, config := range []ScrapeSettings{current, draft} {
		client, err := config.GetTmdbClient()
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		ai := config.GetAiClient()
		defer ai.Close()
		scopes = append(scopes, scope{config, client, ai})
	}
	const filename, prompt = "movie.2024.mkv", "keep the original directory title"
	// 草稿请求之后再次使用已保存的作用域，确保测试连接不会改写业务客户端。
	for _, index := range []int{0, 1, 2, 1} {
		operation := scopes[index]
		if _, err := operation.tmdb.GetMovieDetail(ctx, 1, operation.settings.GetTmdbLanguage()); err != nil {
			t.Fatal(err)
		}
		got := <-requests
		if got.url != operation.settings.GetTmdbApiUrl()+"/3/movie/1" || got.key != operation.settings.GetTmdbApiKey() || got.language != operation.settings.GetTmdbLanguage() {
			t.Fatalf("TMDB 请求未使用捕获的已保存配置：%+v", got)
		}
		if _, err := operation.ai.TakeMoiveName(ctx, filename, prompt); err != nil {
			t.Fatal(err)
		}
		got = <-requests
		if got.url != operation.settings.GetAiBaseUrl()+"/v1/chat/completions" || got.auth != "Bearer "+operation.settings.GetAiApiKey() || got.body.Model != operation.settings.GetAiModelName() || len(got.body.Messages) != 1 || !strings.Contains(got.body.Messages[0].Content, prompt) || !strings.Contains(got.body.Messages[0].Content, filename) {
			t.Fatalf("AI 请求未使用捕获的已保存配置及目录提示词：%+v", got)
		}
	}
	if GetScrapeSettingsSnapshot() != current {
		t.Fatal("草稿连接改变了已发布配置")
	}
}

func TestTMDBAndFanartSaveIsAtomic(t *testing.T) {
	setupHttpProxyTest(t)
	before := GetScrapeSettingsSnapshot()
	if err := db.Db.Exec(`CREATE TRIGGER fail_fanart BEFORE UPDATE OF fanart_api_key ON scrape_settings BEGIN SELECT RAISE(ABORT, 'fanart save rejected'); END`).Error; err != nil {
		t.Fatal(err)
	}
	updated := before
	updated.TmdbApiKey = "new-tmdb-key"
	updated.FanartApiKey = "new-fanart-key"
	if err := SaveTmdbSettings(context.Background(), updated); err == nil {
		t.Fatal("拒绝同页保存时应返回错误")
	}
	if GetScrapeSettingsSnapshot() != before {
		t.Fatal("失败的同页保存改变了有效配置")
	}
	var saved ScrapeSettings
	if err := db.Db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TmdbApiKey != before.TmdbApiKey || saved.FanartApiKey != before.FanartApiKey {
		t.Fatal("同页字段被部分提交")
	}
}

func TestSettingsSlowCommitKeepsReadersAndPreservesOmittedFields(t *testing.T) {
	setupHttpProxyTest(t)
	before := GetThreadSettingsSnapshot()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCommit := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCommit()
	if err := db.Db.Callback().Update().Before("gorm:update").Register("hold_thread_commit", func(tx *gorm.DB) {
		if tx.Statement.Table == "settings" {
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Update().Remove("hold_thread_commit")
	upload, timeout := 3, 20
	first := make(chan error, 1)
	go func() {
		_, err := SaveThreadSettings(context.Background(), ThreadSettingsUpdate{SettingThreads: before.SettingThreads, UploadConcurrency: &upload, UploadRapidWaitTimeoutSeconds: &timeout})
		first <- err
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("保存未进入数据库")
	}
	// 保存调用尚未返回时，读者只观察旧的完整已提交值。
	read := make(chan SettingThreadAndRapidWait, 1)
	go func() { read <- GetThreadSettingsSnapshot() }()
	select {
	case got := <-read:
		if got != before {
			t.Fatal("提交前发布了部分配置")
		}
	case <-time.After(time.Second):
		t.Fatal("SQL阻塞了配置读者")
	}
	releaseCommit()
	if err := <-first; err != nil {
		t.Fatalf("已开始提交应完成发布：%v", err)
	}
	if err := db.Db.Callback().Update().Remove("hold_thread_commit"); err != nil {
		t.Fatal(err)
	}
	// 此命令在旧配置下构造，省略字段仍按拥有者的最新提交合并。
	second := ThreadSettingsUpdate{SettingThreads: before.SettingThreads}
	second.DownloadThreads = 2
	if _, err := SaveThreadSettings(context.Background(), second); err != nil {
		t.Fatal(err)
	}
	current := GetThreadSettingsSnapshot()
	if current.UploadThreads != upload || current.UploadRapidWaitTimeoutSeconds != timeout || current.DownloadThreads != 2 {
		t.Fatalf("省略字段覆盖了最新提交：%+v", current)
	}
	var saved Settings
	if err := db.Db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.ThreadAndRapidWait(), current) {
		t.Fatal("数据库与发布快照不一致")
	}
	if before.UploadThreads == current.UploadThreads {
		t.Fatal("旧操作快照被改变")
	}
}

func TestSettingsCancellationBeforeCommitKeepsSnapshot(t *testing.T) {
	setupHttpProxyTestDatabase(t, filepath.Join(t.TempDir(), "settings.db"))
	before := GetScrapeSettingsSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := db.Db.Callback().Update().Before("gorm:update").Register("cancel_settings_before_commit", func(tx *gorm.DB) { cancel() }); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Update().Remove("cancel_settings_before_commit")
	next := before
	next.TmdbApiKey = "must-not-save"
	if err := SaveTmdbSettings(ctx, next); !errors.Is(err, context.Canceled) {
		t.Fatalf("提交前取消应停止写入：%v", err)
	}
	var saved ScrapeSettings
	if err := db.Db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.TmdbApiKey != before.TmdbApiKey || GetScrapeSettingsSnapshot() != before {
		t.Fatal("取消的写入改变了数据库或有效快照")
	}
}

func TestSettingsCancellationAfterCommitCompletesPublicationAndApply(t *testing.T) {
	setupHttpProxyTest(t)
	before := GetThreadSettingsSnapshot()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := db.Db.Callback().Update().After("gorm:commit_or_rollback_transaction").Register("cancel_settings_after_commit", func(tx *gorm.DB) { cancel() }); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Update().Remove("cancel_settings_after_commit")
	var applied SettingThreadAndRapidWait
	SetThreadSettingsApplyHook(func(settings SettingThreadAndRapidWait) error { applied = settings; return nil })
	upload := 3
	if _, err := SaveThreadSettings(ctx, ThreadSettingsUpdate{SettingThreads: before.SettingThreads, UploadConcurrency: &upload}); err != nil {
		t.Fatalf("已提交写入必须完成发布：%v", err)
	}
	if ctx.Err() != context.Canceled {
		t.Fatal("测试未在提交后取消请求")
	}
	var saved Settings
	if err := db.Db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	current := GetThreadSettingsSnapshot()
	if current.UploadThreads != upload || saved.ThreadAndRapidWait() != current || applied != current {
		t.Fatal("请求取消遗漏已提交的发布或本地应用")
	}
}

func TestSettingsStopRejectsNewWritesAndDrainsAcceptedCommit(t *testing.T) {
	setupHttpProxyTest(t)
	before := GetScrapeSettingsSnapshot()
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	releaseCommit := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseCommit()
	if err := db.Db.Callback().Update().Before("gorm:update").Register("hold_settings_shutdown", func(tx *gorm.DB) { close(entered); <-release }); err != nil {
		t.Fatal(err)
	}
	defer db.Db.Callback().Update().Remove("hold_settings_shutdown")
	updated := before
	updated.TmdbApiKey = "new-key"
	done := make(chan error, 1)
	go func() { done <- SaveTmdbSettings(context.Background(), updated) }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("保存未进入数据库")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := StopSettingsRuntime(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("未完成提交不能宣称退出：%v", err)
	}
	if err := SaveAISettings(context.Background(), ScrapeSettings{}); err == nil {
		t.Fatal("停止接纳后不应接受新写入")
	}
	releaseCommit()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := StopSettingsRuntime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if GetScrapeSettingsSnapshot().TmdbApiKey != updated.TmdbApiKey {
		t.Fatal("退出遗漏已接收的发布")
	}
}
