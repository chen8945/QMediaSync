package models

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func setupPreloadSettings(t *testing.T) {
	t.Helper()
	setupFreshInitializationSQLite(t)
	initializePreloadSettings(t)
}

func initializePreloadSettings(t *testing.T) {
	t.Helper()
	previous, snapshot, hook := SettingsGlobal, playbackSettings.Load(), playbackSettingsHook.Load()
	t.Cleanup(func() {
		SettingsGlobal = previous
		playbackSettings.Store(snapshot)
		playbackSettingsHook.Store(hook)
	})
	SetPlaybackSettingsChangeHook(nil)
	if err := db.Db.AutoMigrate(&Settings{}); err != nil {
		t.Fatal(err)
	}
	SettingsGlobal = &Settings{Cron: "0 * * * *", StrmBaseUrl: "http://qms.invalid"}
	if err := db.Db.Create(SettingsGlobal).Error; err != nil {
		t.Fatal(err)
	}
	LoadSettings()
}

func TestPreloadSettingsDefaultAndAtomicPublication(t *testing.T) {
	setupPreloadSettings(t)
	testPreloadSettingsAtomicPublication(t)
}

func testPreloadSettingsAtomicPublication(t *testing.T) {
	t.Helper()
	initial := GetPlaybackSettingsSnapshot()
	if initial.PreloadEnabled || initial.Generation == 0 {
		t.Fatalf("新设置应默认关闭：%+v", initial)
	}
	playbackSettings.Store(nil)
	if got := GetPlaybackSettingsSnapshot(); got != (PlaybackSettingsSnapshot{}) {
		t.Fatalf("未加载配置不应启用预加载：%+v", got)
	}
	LoadSettings()
	calls := 0
	SetPlaybackSettingsChangeHook(func(current PlaybackSettingsSnapshot) {
		calls++
		// 回调能读取设置并查询已提交的数据库，证明不在写锁或事务内。
		_, _, enabled := SettingsGlobal.StrmConfigSnapshot()
		var stored Settings
		if err := db.Db.First(&stored).Error; err != nil || (enabled == 1) != current.PreloadEnabled || stored.StrmPreloadEnabled != enabled {
			t.Errorf("通知发生在提交之前：%+v %v", stored, err)
		}
	})
	strm, _ := SettingsGlobal.StrmSnapshot()
	enabled := 1
	if !SettingsGlobal.UpdateStrm(strm, 1, &enabled) {
		t.Fatal("启用失败")
	}
	published := GetPlaybackSettingsSnapshot()
	if !published.PreloadEnabled || !published.MultiPlaybackEnabled || calls != 1 {
		t.Fatalf("成功保存未发布完整配置：%+v calls=%d", published, calls)
	}
	LoadSettings()
	if !SettingsGlobal.UpdateStrm(strm, 1) || GetPlaybackSettingsSnapshot() != published || calls != 1 {
		t.Fatal("省略字段或相同重载不应覆盖开关或推进代际")
	}
	strm.StrmBaseUrl = "http://changed.invalid"
	if !SettingsGlobal.UpdateStrm(strm, 1) {
		t.Fatal("更新 STRM 来源失败")
	}
	changed := GetPlaybackSettingsSnapshot()
	if changed.Generation <= published.Generation || changed.StrmBaseURL != strm.StrmBaseUrl || calls != 2 {
		t.Fatalf("来源变化未发布新代际：%+v calls=%d", changed, calls)
	}
	published = changed
	failure := errors.New("test settings write failure")
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:preload_write", func(tx *gorm.DB) { tx.AddError(failure) }); err != nil {
		t.Fatal(err)
	}
	disabled := 0
	strm.StrmBaseUrl = "http://failed.invalid"
	if SettingsGlobal.UpdateStrm(strm, 0, &disabled) || GetPlaybackSettingsSnapshot() != published || calls != 2 {
		t.Fatal("失败保存不应改变运行态或通知")
	}
	if err := db.Db.Callback().Update().Remove("test:preload_write"); err != nil {
		t.Fatal(err)
	}
	strm.StrmBaseUrl = published.StrmBaseURL
	if !SettingsGlobal.UpdateStrm(strm, 0, &disabled) {
		t.Fatal("显式关闭失败")
	}
	closed := GetPlaybackSettingsSnapshot()
	if closed.PreloadEnabled || closed.Generation <= published.Generation || calls != 3 {
		t.Fatalf("关闭未发布新代际：%+v calls=%d", closed, calls)
	}
	if err := db.Db.Callback().Query().Before("gorm:query").Register("test:preload_read", func(tx *gorm.DB) { tx.AddError(failure) }); err != nil {
		t.Fatal(err)
	}
	LoadSettings()
	if GetPlaybackSettingsSnapshot() != closed || calls != 3 {
		t.Fatal("加载失败不应改变运行态")
	}
}

func TestPreloadSettingsReadsDoNotWaitForSQLAndOmissionMergesUnderLock(t *testing.T) {
	setupPreloadSettings(t)
	initial := GetPlaybackSettingsSnapshot()
	entered, release := make(chan struct{}), make(chan struct{})
	blocked := true
	if err := db.Db.Callback().Update().Before("gorm:update").Register("test:preload_block", func(*gorm.DB) {
		if blocked {
			blocked = false
			close(entered)
			<-release
		}
	}); err != nil {
		t.Fatal(err)
	}
	strm, _ := SettingsGlobal.StrmSnapshot()
	saved := make(chan bool, 2)
	go func() { enabled := 1; saved <- SettingsGlobal.UpdateStrm(strm, 0, &enabled) }()
	<-entered
	go func() { saved <- SettingsGlobal.UpdateStrm(strm, 0, nil) }()
	read := make(chan PlaybackSettingsSnapshot, 1)
	go func() { read <- GetPlaybackSettingsSnapshot() }()
	select {
	case got := <-read:
		if got != initial {
			t.Error("SQL 提交前发布了运行态")
		}
	case <-time.After(time.Second):
		t.Error("播放配置读取被慢 SQL 阻塞")
	}
	close(release)
	for range 2 {
		if !<-saved {
			t.Fatal("并发保存失败")
		}
	}
	var stored Settings
	if err := db.Db.First(&stored).Error; err != nil || stored.StrmPreloadEnabled != 1 || !GetPlaybackSettingsSnapshot().PreloadEnabled {
		t.Fatalf("省略字段丢失了并发写入：%+v %v", stored, err)
	}
}
