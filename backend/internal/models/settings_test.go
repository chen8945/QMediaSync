package models

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"reflect"
	"sync"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
)

func TestSettingStrmRegexRoundTrip(t *testing.T) {
	patterns := []string{"(?i)^Sample\\.[^.]+$", "^A{1,3};B$", " \\QAbC\\E "}
	setting := SettingStrm{
		ExcludeNameArr:      []string{"SaMpLe"},
		ExcludeNameRegexArr: append([]string(nil), patterns...),
	}
	encoded := setting.EncodeArr()
	if encoded == nil {
		t.Fatal("编码 STRM 设置失败")
	}
	if !reflect.DeepEqual(encoded.ExcludeNameArr, []string{"sample"}) {
		t.Fatalf("原排除名称应继续转小写，实际为 %q", encoded.ExcludeNameArr)
	}
	stored := encoded.ToMap(true, true)
	decoded := (SettingStrm{
		ExcludeName:      stored["exclude_name"].(string),
		ExcludeNameRegex: stored["exclude_name_regex"].(string),
	}).DecodeArr(true)
	if decoded == nil {
		t.Fatal("解码 STRM 设置失败")
	}
	if !reflect.DeepEqual(decoded.ExcludeNameRegexArr, patterns) {
		t.Fatalf("正则落库再加载后变为 %q，期望原文 %q", decoded.ExcludeNameRegexArr, patterns)
	}
	if got := decoded.ToMap(false, true)["exclude_name_regex_arr"]; !reflect.DeepEqual(got, patterns) {
		t.Fatalf("API 正则数组 = %q，期望 %q", got, patterns)
	}
}

func TestSettingStrmLegacyRegexDefaultsToEmpty(t *testing.T) {
	for _, isSetting := range []bool{false, true} {
		name := "同步目录"
		if isSetting {
			name = "全局设置"
		}
		t.Run(name, func(t *testing.T) {
			decoded := (SettingStrm{ExcludeName: "[\"sample\"]"}).DecodeArr(isSetting)
			if decoded == nil || decoded.ExcludeNameRegexArr == nil || len(decoded.ExcludeNameRegexArr) != 0 {
				t.Fatalf("旧设置应返回空正则数组，实际为 %+v", decoded)
			}
			if !reflect.DeepEqual(decoded.ExcludeNameArr, []string{"sample"}) {
				t.Fatalf("旧排除名称被修改：%q", decoded.ExcludeNameArr)
			}
		})
	}
}

func TestSyncPathRegexInheritance(t *testing.T) {
	originalSettings := SettingsGlobal
	t.Cleanup(func() { SettingsGlobal = originalSettings })
	SettingsGlobal = &Settings{SettingStrm: SettingStrm{
		ExcludeNameRegexArr: []string{"(?i)global"},
	}}
	path := &SyncPath{CustomConfig: true, SettingStrm: GetStrmSettingDefault()}
	if got := path.GetExcludeNameRegexArr(); !reflect.DeepEqual(got, []string{"(?i)global"}) {
		t.Fatalf("空自定义列表应继承全局，实际为 %q", got)
	}
	path.ExcludeNameRegexArr = []string{"^Custom$"}
	if got := path.GetExcludeNameRegexArr(); !reflect.DeepEqual(got, []string{"^Custom$"}) {
		t.Fatalf("非空自定义列表应覆盖全局，实际为 %q", got)
	}
}

func TestStrmSnapshotOwnsMutableLists(t *testing.T) {
	settings := &Settings{
		MultiPlaybackEnabled: 1,
		SettingStrm: SettingStrm{
			VideoExtArr: []string{".mkv"}, MetaExtArr: []string{".nfo"},
			ExcludeNameArr: []string{"sample"}, ExcludeNameRegexArr: []string{"^sample$"},
		},
	}
	snapshot, enabled := settings.StrmSnapshot()
	if enabled != 1 {
		t.Fatal("快照未保留多端播放开关")
	}
	for _, pair := range []struct{ snapshot, original []string }{
		{snapshot.VideoExtArr, settings.VideoExtArr},
		{snapshot.MetaExtArr, settings.MetaExtArr},
		{snapshot.ExcludeNameArr, settings.ExcludeNameArr},
		{snapshot.ExcludeNameRegexArr, settings.ExcludeNameRegexArr},
	} {
		original := pair.original[0]
		pair.snapshot[0] = "changed"
		if pair.original[0] != original {
			t.Fatal("快照列表不能与运行时设置共用可变存储")
		}
	}
}

func TestPlaybackSettingsConcurrentSaveReloadAndSnapshot(t *testing.T) {
	originalDB, originalSettings, originalLogger := db.Db, SettingsGlobal, helpers.AppLogger
	testDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() {
		db.Db, SettingsGlobal, helpers.AppLogger = originalDB, originalSettings, originalLogger
		_ = sqlDB.Close()
	})
	db.Db = testDB
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	if err := db.Db.AutoMigrate(&Settings{}); err != nil {
		t.Fatal(err)
	}
	SettingsGlobal = &Settings{}
	if err := db.Db.Create(SettingsGlobal).Error; err != nil {
		t.Fatal(err)
	}
	config := func(enabled int) SettingStrm {
		return SettingStrm{
			LocalProxy: enabled, Cron: fmt.Sprintf("%d * * * *", enabled), StrmBaseUrl: "http://qms.local",
			VideoExtArr: []string{".mkv"}, MetaExtArr: []string{".nfo"},
			ExcludeNameArr: []string{fmt.Sprint(enabled)}, ExcludeNameRegexArr: []string{fmt.Sprintf("^%d$", enabled)},
		}
	}
	if !SettingsGlobal.UpdateStrm(config(0), 0) {
		t.Fatal("初始化 STRM 设置失败")
	}
	// 拒绝线程更新，以覆盖其全结构复制和失败解锁，不启动无关下载队列。
	if err := db.Db.Exec(`CREATE TRIGGER fail_threads BEFORE UPDATE OF upload_threads ON settings BEGIN SELECT RAISE(ABORT, 'test write failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errors := make(chan error, 5)
	var workers sync.WaitGroup
	workers.Go(func() {
		<-start
		for i := range 80 {
			if !SettingsGlobal.UpdateStrm(config(i%2), i%2) {
				errors <- fmt.Errorf("第 %d 次保存失败", i)
				return
			}
		}
	})
	workers.Go(func() {
		<-start
		for range 80 {
			LoadSettings()
		}
	})
	workers.Go(func() {
		<-start
		for range 40 {
			if SettingsGlobal.UpdateThreads(SettingThreadAndRapidWait{}) {
				errors <- fmt.Errorf("线程写入失败不应返回成功")
				return
			}
		}
	})
	for range 2 {
		workers.Go(func() {
			<-start
			for range 400 {
				proxy, enabled := GetPlaybackSettings()
				if (proxy == 1) != enabled {
					errors <- fmt.Errorf("播放配置混合了不同次发布：proxy=%d, enabled=%t", proxy, enabled)
					return
				}
				strm, flag := SettingsGlobal.StrmSnapshot()
				if strm.LocalProxy != flag || strm.Cron != fmt.Sprintf("%d * * * *", flag) ||
					len(strm.ExcludeNameArr) != 1 || strm.ExcludeNameArr[0] != fmt.Sprint(flag) {
					errors <- fmt.Errorf("STRM 快照混合了不同次发布")
					return
				}
				if _, err := json.Marshal(strm.ToMap(false, true)); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	close(start)
	workers.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	proxy, enabled := GetPlaybackSettings()
	if proxy != 1 || !enabled {
		t.Fatal("并发重载覆盖了最后一次成功保存")
	}
}
