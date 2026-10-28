package playback

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPreloadUAMappingsExactIdentityIdleAndGeneration(t *testing.T) {
	now := time.Unix(1000, 0)
	m := PreloadUAMappings{now: func() time.Time { return now }}
	m.Remember("device/control-A/1", "Player B/1.0 ", 1)
	for _, key := range []string{"device/control-A/2", "device/control-a/1", "device/control-A/1 ", "other/control-A/1"} {
		if got := m.Lookup(key, 1); got != "" {
			t.Fatalf("不同完整控制身份共享映射：key=%q UA=%q", key, got)
		}
	}
	now = now.Add(71 * time.Hour)
	if got := m.Lookup("device/control-A/1", 1); got != "Player B/1.0 " {
		t.Fatalf("UA 被归一化或未保留72小时：%q", got)
	}
	now = now.Add(71 * time.Hour)
	if got := m.Lookup("device/control-A/1", 1); got != "Player B/1.0 " {
		t.Fatalf("真实活动没有续期：%q", got)
	}
	m.Remember("device/control-A/1", "Player B/2.0", 1)
	if got := m.Lookup("device/control-A/1", 1); got != "Player B/2.0" {
		t.Fatalf("播放器更新未替换完整UA：%q", got)
	}
	now = now.Add(72 * time.Hour)
	if got := m.Lookup("device/control-A/1", 1); got != "" {
		t.Fatalf("闲置72小时仍可用：%q", got)
	}
	m.Remember("current", "Player/3", 2)
	m.Remember("old", "Player/1", 1)
	if m.Lookup("current", 1) != "" || m.Lookup("current", 2) != "Player/3" || m.Lookup("old", 2) != "" {
		t.Fatal("旧代际清除或污染了新映射")
	}
	if m.Lookup("current", 3) != "" {
		t.Fatal("配置代际变化后仍使用旧映射")
	}
	m.Remember("current", "Player/4", 3)
	m.Clear()
	if m.Lookup("current", 3) != "" {
		t.Fatal("实例停止未清理映射")
	}
}

func TestPreloadUAMappingsCapacityEvictsInactiveAndRejectsInvalid(t *testing.T) {
	now := time.Unix(1000, 0)
	m := PreloadUAMappings{now: func() time.Time { return now }}
	for index := range preloadUAMappingCapacity {
		m.Remember(fmt.Sprint(index), "Player/1", 1)
		now = now.Add(time.Second)
	}
	_ = m.Lookup("0", 1)
	now = now.Add(time.Second)
	m.Remember("new", "Player/2", 1)
	if len(m.entries) != preloadUAMappingCapacity || m.Lookup("0", 1) == "" || m.Lookup("1", 1) != "" || m.Lookup("new", 1) != "Player/2" {
		t.Fatal("容量满时没有淘汰最久未活动映射")
	}
	for _, ua := range []string{"", "Player\r\nInjected: 1", "Player\x00bad"} {
		m.Remember("invalid", ua, 1)
	}
	if m.Lookup("invalid", 1) != "" {
		t.Fatal("无效UA进入映射")
	}
}

func TestPreloadUAMappingsConcurrentUse(t *testing.T) {
	var m PreloadUAMappings
	var wait sync.WaitGroup
	for index := range 64 {
		wait.Go(func() {
			key := fmt.Sprint(index)
			for range 50 {
				m.Remember(key, "Player/1", 1)
				if got := m.Lookup(key, 1); got != "Player/1" {
					t.Errorf("并发读写丢失映射：%q", got)
				}
			}
		})
	}
	wait.Wait()
}
