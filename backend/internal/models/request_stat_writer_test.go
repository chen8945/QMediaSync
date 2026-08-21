package models

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"qmediasync/internal/db"
)

func TestNewRequestStatWriterUsesConfiguredQueueCapacity(t *testing.T) {
	writer := NewRequestStatWriter()
	writer.Close()

	if got := cap(writer.queue); got != 2048 {
		t.Fatalf("默认请求统计队列容量 = %d，期望 2048", got)
	}
}

func TestRequestStatWriterFlushesQueuedStatsInBatches(t *testing.T) {
	var mu sync.Mutex
	var batches [][]RequestStat

	writer := newRequestStatWriter(8, 2, time.Hour, func(stats []*RequestStat) error {
		batch := make([]RequestStat, len(stats))
		for i, stat := range stats {
			batch[i] = *stat
		}
		mu.Lock()
		batches = append(batches, batch)
		mu.Unlock()
		return nil
	})

	for i := int64(1); i <= 5; i++ {
		writer.Enqueue(i, "/api/test", "GET", i*10, false)
	}
	writer.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 3 {
		t.Fatalf("批量写入次数 = %d，期望 3", len(batches))
	}
	if len(batches[0]) != 2 || len(batches[1]) != 2 || len(batches[2]) != 1 {
		t.Fatalf("批量大小 = [%d %d %d]，期望 [2 2 1]", len(batches[0]), len(batches[1]), len(batches[2]))
	}
	if got := writer.DroppedCount(); got != 0 {
		t.Fatalf("正常队列容量下丢弃数量 = %d，期望 0", got)
	}
}

func TestRequestStatWriterDropsWhenQueueIsFull(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once

	writer := newRequestStatWriter(1, 1, time.Hour, func([]*RequestStat) error {
		once.Do(func() { close(started) })
		<-release
		return nil
	})

	writer.Enqueue(1, "/api/test", "GET", 1, false)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("统计 worker 未开始处理首条记录")
	}

	writer.Enqueue(2, "/api/test", "GET", 2, false)
	writer.Enqueue(3, "/api/test", "GET", 3, false)
	if got := writer.DroppedCount(); got != 1 {
		t.Fatalf("队列满时丢弃数量 = %d，期望 1", got)
	}

	close(release)
	writer.Close()
}

func TestRequestStatWriterPersistsStats(t *testing.T) {
	oldDB := db.Db
	testDB, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "request-stats.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开请求统计测试数据库失败: %v", err)
	}
	db.Db = testDB
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatalf("获取请求统计测试连接失败: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
		db.Db = oldDB
	})
	if err := db.Db.AutoMigrate(&RequestStat{}); err != nil {
		t.Fatalf("创建请求统计表失败: %v", err)
	}

	writer := NewRequestStatWriter()
	writer.Enqueue(1_800_000_000, "/api/test", "GET", 42, true)
	writer.Enqueue(1_800_000_001, "/api/test", "POST", 84, false)
	writer.Close()

	var stats []RequestStat
	if err := db.Db.Order("request_time ASC").Find(&stats).Error; err != nil {
		t.Fatalf("读取批量写入的请求统计失败: %v", err)
	}
	if len(stats) != 2 {
		t.Fatalf("批量写入统计数量 = %d，期望 2", len(stats))
	}
	if stats[0].RequestTime != 1_800_000_000 || stats[0].Duration != 42 || !stats[0].IsThrottled {
		t.Fatalf("首条请求统计内容错误: %#v", stats[0])
	}
	if stats[1].RequestTime != 1_800_000_001 || stats[1].Method != "POST" || stats[1].IsThrottled {
		t.Fatalf("第二条请求统计内容错误: %#v", stats[1])
	}
}
