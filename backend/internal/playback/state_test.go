package playback

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/coocood/freecache"

	"qmediasync/internal/db"
)

func TestManagerSlotIdentityAndExpiry(t *testing.T) {
	now := time.Now()
	source := SourceKey{AccountID: 1, UserID: "115-user", PickCode: "original"}
	current := Slot{Mode: ModeDirect, UA: "Player A"}
	for _, tt := range []struct {
		name   string
		source SourceKey
		slot   Slot
		at     time.Time
		want   bool
	}{
		{name: "同 UA 和模式复用", source: source, slot: current, at: now},
		{name: "不同 UA 独立", source: source, slot: Slot{Mode: ModeDirect, UA: "Player B"}, at: now, want: true},
		{name: "同 UA 不同模式独立", source: source, slot: Slot{Mode: ModeProxy, UA: current.UA}, at: now, want: true},
		{name: "账号隔离", source: SourceKey{AccountID: 2, UserID: source.UserID, PickCode: source.PickCode}, at: now},
		{name: "更换 115 用户后隔离", source: SourceKey{AccountID: 1, UserID: "other-user", PickCode: source.PickCode}, at: now},
		{name: "原文件隔离", source: SourceKey{AccountID: 1, UserID: source.UserID, PickCode: "other"}, at: now},
		{name: "到期前仍有效", source: source, at: now.Add(50*time.Minute - time.Nanosecond), want: true},
		{name: "恰好到期失效", source: source, at: now.Add(50 * time.Minute)},
		{name: "过期失效", source: source, at: now.Add(time.Hour)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := NewManager()
			manager.Record(source, current, now.Add(URLCacheTTL))
			if got := manager.HasOther(tt.source, tt.slot, tt.at); got != tt.want {
				t.Fatalf("HasOther() = %v，期望 %v", got, tt.want)
			}
		})
	}
}

func TestManagerSlotSurvivesURLCacheEvictionWithoutExtendingExpiry(t *testing.T) {
	manager := NewManager()
	cache := db.CacheGlobal{CacheInstance: freecache.NewCache(1 << 20)}
	source := SourceKey{AccountID: 1, UserID: "115-user", PickCode: "original"}
	slot := Slot{Mode: ModeDirect, UA: "Player A"}
	other := Slot{Mode: ModeDirect, UA: "Player B"}
	now := time.Now()
	cache.Set("url", []byte("https://cdn.test/copy"), 3000)
	manager.Record(source, slot, now.Add(URLCacheTTL))
	cache.Delete("url")
	if cache.Get("url") != nil {
		t.Fatal("测试 URL 应已被提前删除")
	}
	for _, at := range []time.Time{now, now.Add(25 * time.Minute), now.Add(49 * time.Minute)} {
		if !manager.HasOther(source, other, at) {
			t.Fatal("URL 缓存提前删除不应丢失未过期槽位")
		}
	}
	if manager.HasOther(source, other, now.Add(50*time.Minute)) {
		t.Fatal("读取槽位不能延长原始过期时间")
	}
}

func TestManagerConcurrentRecordsKeepEverySlot(t *testing.T) {
	manager := NewManager()
	source := SourceKey{AccountID: 1, UserID: "115-user", PickCode: "original"}
	expires := time.Now().Add(URLCacheTTL)
	const total = 64
	var workers sync.WaitGroup
	for i := range total {
		workers.Go(func() {
			manager.Record(source, Slot{Mode: ModeDirect, UA: fmt.Sprintf("Player %d", i)}, expires)
			manager.HasOther(source, Slot{Mode: ModeProxy}, time.Now())
		})
	}
	workers.Wait()
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if len(manager.slots[source]) != total {
		t.Fatalf("并发登记后仅剩 %d 个槽位，期望 %d", len(manager.slots[source]), total)
	}
	for i := range total {
		slot := Slot{Mode: ModeDirect, UA: fmt.Sprintf("Player %d", i)}
		if !manager.slots[source][slot].Equal(expires) {
			t.Fatalf("槽位 %s 的期限丢失或被覆盖", slot.UA)
		}
	}
}

func TestManagerBeginReservesBeforeConcurrentNetworkWork(t *testing.T) {
	manager := NewManager()
	source := SourceKey{AccountID: 1, UserID: "user", PickCode: "file"}
	const total = 64
	results := make(chan bool, total)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for i := range total {
		workers.Go(func() {
			<-start
			results <- manager.Begin(source, Slot{Mode: ModeDirect, UA: fmt.Sprint(i)}, time.Now())
		})
	}
	close(start)
	workers.Wait()
	close(results)
	originals := 0
	for copyNeeded := range results {
		if !copyNeeded {
			originals++
		}
	}
	if originals != 1 {
		t.Fatalf("尚未发布 URL 时也必须只有一个原文件请求，实际 %d", originals)
	}
	for i := range total {
		manager.Finish(source, Slot{Mode: ModeDirect, UA: fmt.Sprint(i)}, time.Time{})
	}
	if manager.HasOther(source, Slot{UA: "observer"}, time.Now()) {
		t.Fatal("所有失败预留撤销后不能残留槽位")
	}
}

func TestManagerPendingCountsAndPublishesAtomically(t *testing.T) {
	manager := NewManager()
	source := SourceKey{AccountID: 1, UserID: "user", PickCode: "file"}
	slot := Slot{Mode: ModeDirect, UA: "player"}
	observer := Slot{Mode: ModeDirect, UA: "observer"}
	now := time.Now()
	if manager.Begin(source, slot, now) || manager.Begin(source, slot, now) {
		t.Fatal("同一 UA 不应将自己的另一个预留识别为多端")
	}
	manager.Finish(source, slot, time.Time{})
	if !manager.HasOther(source, observer, now) {
		t.Fatal("一次失败不能撤销同槽位另一条在途请求")
	}
	expiresAt := now.Add(URLCacheTTL)
	manager.Finish(source, slot, expiresAt)
	if !manager.HasOther(source, observer, now) || manager.HasOther(source, observer, expiresAt) {
		t.Fatal("预留发布后应只按缓存到期时间判断")
	}
	if len(manager.pending) != 0 {
		t.Fatal("完成发布后应释放预留计数")
	}
}

func TestManagerPendingIdentityAndFailurePreserveExistingSlot(t *testing.T) {
	manager := NewManager()
	source := SourceKey{AccountID: 1, UserID: "user", PickCode: "file"}
	slot := Slot{Mode: ModeDirect, UA: "player"}
	now := time.Now()
	manager.Begin(source, slot, now)
	for _, otherSource := range []SourceKey{
		{AccountID: 2, UserID: source.UserID, PickCode: source.PickCode},
		{AccountID: source.AccountID, UserID: "replacement", PickCode: source.PickCode},
		{AccountID: source.AccountID, UserID: source.UserID, PickCode: "other"},
	} {
		if manager.HasOther(otherSource, Slot{Mode: ModeProxy}, now) {
			t.Fatalf("预留未隔离原文件或账号：%+v", otherSource)
		}
	}
	expiresAt := now.Add(URLCacheTTL)
	manager.Record(source, slot, expiresAt)
	manager.Finish(source, slot, time.Time{})
	if !manager.HasOther(source, Slot{Mode: ModeProxy}, now) || manager.HasOther(source, Slot{Mode: ModeProxy}, expiresAt) {
		t.Fatal("重取链失败只能撤销预留，不能删除或续期旧槽位")
	}
}

func TestURLExpiresAt(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	for _, tt := range []struct {
		name    string
		url     string
		want    time.Time
		expired bool
	}{
		{name: "缺少签名时间沿用50分钟", url: "https://cdn.test/video", want: now.Add(URLCacheTTL)},
		{name: "60分钟签名仍受缓存上限约束", url: fmt.Sprintf("https://cdn.test/video?t=%d", now.Add(time.Hour).Unix()), want: now.Add(URLCacheTTL)},
		{name: "较短签名提前5分钟过期", url: fmt.Sprintf("https://cdn.test/video?t=%d", now.Add(20*time.Minute).Unix()), want: now.Add(15 * time.Minute)},
		{name: "签名已进入安全窗口", url: fmt.Sprintf("https://cdn.test/video?t=%d", now.Add(5*time.Minute).Unix()), expired: true},
		{name: "签名早已过期", url: "https://cdn.test/video?t=1", expired: true},
		{name: "负数签名已失效", url: "https://cdn.test/video?t=-9223372036854775808", expired: true},
		{name: "极大合法整数不能溢出", url: "https://cdn.test/video?t=9223372036854775807", want: now.Add(URLCacheTTL)},
		{name: "畸形时间沿用上限", url: "https://cdn.test/video?t=unknown", want: now.Add(URLCacheTTL)},
		{name: "无法解析URL沿用上限", url: "https://[invalid", want: now.Add(URLCacheTTL)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := URLExpiresAt(tt.url, now)
			if tt.expired {
				if got.After(now) {
					t.Fatalf("已失效 URL 不可缓存到 %v", got)
				}
			} else if !got.Equal(tt.want) {
				t.Fatalf("URLExpiresAt() = %v，期望 %v", got, tt.want)
			}
		})
	}
}
