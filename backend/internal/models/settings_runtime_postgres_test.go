//go:build integration

package models

import (
	"context"
	"testing"

	"qmediasync/internal/db"
)

func TestSettingsPostgresCommittedValuesAndAtomicFailure(t *testing.T) {
	// 复用已有按连接池隔离schema的fixture；该fixture不初始化媒体业务表。
	conn, _ := setupEmbySnapshotPostgres(t)
	oldSettings, oldSnapshot := SettingsGlobal, committedSettings.Load()
	oldDownload, oldUpload := GlobalDownloadQueue, GlobalUploadQueue
	oldHook := threadSettingsApply.Load()
	GlobalDownloadQueue, GlobalUploadQueue = nil, nil
	threadSettingsApply.Store(nil)
	if err := conn.AutoMigrate(&Settings{}, &ScrapeSettings{}); err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&Settings{DownloadThreads: 1, UploadThreads: 2, FileDetailThreads: 3}).Error; err != nil {
		t.Fatal(err)
	}
	if err := conn.Create(&ScrapeSettings{TmdbApiKey: "old", FanartApiKey: "old-fanart"}).Error; err != nil {
		t.Fatal(err)
	}
	LoadSettings()
	LoadScrapeSettings()
	if err := StartSettingsRuntime(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = StopSettingsRuntime(context.Background())
		SettingsGlobal = oldSettings
		committedSettings.Store(oldSnapshot)
		GlobalDownloadQueue, GlobalUploadQueue = oldDownload, oldUpload
		threadSettingsApply.Store(oldHook)
	})
	ctx := context.Background()
	next := GetScrapeSettingsSnapshot()
	next.TmdbApiKey = "committed"
	next.FanartApiKey = "committed-fanart"
	if err := SaveTmdbSettings(ctx, next); err != nil {
		t.Fatal(err)
	}
	if current := GetScrapeSettingsSnapshot(); current.TmdbApiKey != next.TmdbApiKey || current.FanartApiKey != next.FanartApiKey {
		t.Fatal("成功提交未发布完整同页配置")
	}
	if err := conn.Exec(`ALTER TABLE scrape_settings ADD CONSTRAINT reject_tmdb_key CHECK (tmdb_api_key <> 'reject')`).Error; err != nil {
		t.Fatal(err)
	}
	rejected := next
	rejected.TmdbApiKey = "reject"
	rejected.FanartApiKey = "should-not-save"
	if err := SaveTmdbSettings(ctx, rejected); err == nil {
		t.Fatal("数据库约束失败应返回错误")
	}
	var saved ScrapeSettings
	if err := db.Db.First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	current := GetScrapeSettingsSnapshot()
	if saved.TmdbApiKey != next.TmdbApiKey || saved.FanartApiKey != next.FanartApiKey || current.TmdbApiKey != next.TmdbApiKey || current.FanartApiKey != next.FanartApiKey {
		t.Fatal("失败更新改变了提交值或运行时")
	}
	if err := conn.Delete(&ScrapeSettings{}, next.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := SaveAISettings(ctx, ScrapeSettings{AiApiKey: "unmatched"}); err == nil {
		t.Fatal("不存在记录不应报告保存成功")
	}
	if GetScrapeSettingsSnapshot() != current {
		t.Fatal("无匹配更新不应改变有效快照")
	}
}
