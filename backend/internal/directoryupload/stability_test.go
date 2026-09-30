package directoryupload

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"qmediasync/internal/models"
)

type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.now = c.now.Add(d)
}

func TestStabilityQueueWaitsForStableFile(t *testing.T) {
	clock := &fakeClock{now: time.Unix(100, 0)}
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	writeFileWithMtime(t, path, []byte("one"), clock.Now())

	queue := NewStabilityQueue(StabilityQueueOptions{Now: clock.Now})
	rule := &models.DirectoryUploadRule{
		ID: 1,
	}
	queue.Track(rule.ID, path)

	ready, err := queue.Check(rule)
	if err != nil {
		t.Fatalf("首次稳定性检查失败: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("首次检查不应入队，got=%v", ready)
	}

	clock.Add(time.Second)
	writeFileWithMtime(t, path, []byte("changed"), clock.Now())
	ready, err = queue.Check(rule)
	if err != nil {
		t.Fatalf("变化后稳定性检查失败: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("文件大小变化后不应入队，got=%v", ready)
	}

	clock.Add(15 * time.Second)
	ready, err = queue.Check(rule)
	if err != nil {
		t.Fatalf("稳定窗口首次检查失败: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("稳定计数未满足时不应入队，got=%v", ready)
	}

	ready, err = queue.Check(rule)
	if err != nil {
		t.Fatalf("稳定窗口第二次检查失败: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("稳定计数未满足时不应入队，got=%v", ready)
	}

	ready, err = queue.Check(rule)
	if err != nil {
		t.Fatalf("稳定窗口第三次检查失败: %v", err)
	}
	if len(ready) != 1 || ready[0].Path != path {
		t.Fatalf("ready=%v，期望稳定文件 %s", ready, path)
	}
}

func TestStabilityQueueResetsWhenSignatureChanges(t *testing.T) {
	clock := &fakeClock{now: time.Unix(200, 0)}
	dir := t.TempDir()
	path := filepath.Join(dir, "episode.mkv")
	writeFileWithMtime(t, path, []byte("one"), clock.Now())

	queue := NewStabilityQueue(StabilityQueueOptions{Now: clock.Now})
	rule := &models.DirectoryUploadRule{
		ID: 2,
	}
	queue.Track(rule.ID, path)

	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次检查 ready=%v err=%v，期望未稳定", ready, err)
	}
	clock.Add(time.Second)
	writeFileWithMtime(t, path, []byte("one"), clock.Now())
	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("mtime 变化后 ready=%v err=%v，期望重置稳定计数", ready, err)
	}
	clock.Add(15 * time.Second)
	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("签名连续不变后 ready=%v err=%v，期望稳定计数未满足", ready, err)
	}
	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("签名连续不变后 ready=%v err=%v，期望稳定计数未满足", ready, err)
	}
	if ready, err := queue.Check(rule); err != nil || len(ready) != 1 {
		t.Fatalf("签名连续不变后 ready=%v err=%v，期望入队", ready, err)
	}
}

func TestStabilityQueueUsesBuiltInWindowAndCount(t *testing.T) {
	clock := &fakeClock{now: time.Unix(300, 0)}
	dir := t.TempDir()
	path := filepath.Join(dir, "movie.mkv")
	writeFileWithMtime(t, path, []byte("one"), clock.Now())

	queue := NewStabilityQueue(StabilityQueueOptions{Now: clock.Now})
	rule := &models.DirectoryUploadRule{
		ID:                     3,
		StabilitySeconds:       3600,
		StabilityRequiredCount: 99,
	}
	queue.Track(rule.ID, path)

	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次检查 ready=%v err=%v，期望未稳定", ready, err)
	}

	clock.Add(15 * time.Second)
	for i := 1; i < 3; i++ {
		ready, err := queue.Check(rule)
		if err != nil {
			t.Fatalf("第 %d 次内置稳定检查失败: %v", i, err)
		}
		if len(ready) != 0 {
			t.Fatalf("第 %d 次内置稳定检查 ready=%v，期望计数未满足", i, ready)
		}
	}

	ready, err := queue.Check(rule)
	if err != nil {
		t.Fatalf("内置稳定窗口检查失败: %v", err)
	}
	if len(ready) != 1 || ready[0].Path != path {
		t.Fatalf("ready=%v，期望按内置 15s/3 次稳定规则入队 %s", ready, path)
	}
}

func TestStabilityQueueDropsSymlinkWhenTargetEscapesMonitor(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 下 symlink 权限和路径分隔符语义不同")
	}
	clock := &fakeClock{now: time.Unix(400, 0)}
	monitorPath := t.TempDir()
	insideTarget := filepath.Join(monitorPath, "inside.mkv")
	outsideTarget := filepath.Join(t.TempDir(), "outside.mkv")
	linkPath := filepath.Join(monitorPath, "linked.mkv")
	writeFileWithMtime(t, insideTarget, []byte("inside"), clock.Now())
	writeFileWithMtime(t, outsideTarget, []byte("outside"), clock.Now())
	if err := os.Symlink(insideTarget, linkPath); err != nil {
		t.Skipf("当前平台不支持创建 symlink: %v", err)
	}

	queue := NewStabilityQueue(StabilityQueueOptions{Now: clock.Now})
	rule := &models.DirectoryUploadRule{
		ID:          4,
		MonitorPath: monitorPath,
	}
	queue.Track(rule.ID, linkPath)

	if ready, err := queue.Check(rule); err != nil || len(ready) != 0 {
		t.Fatalf("首次检查 ready=%v err=%v，期望未稳定", ready, err)
	}
	if err := os.Remove(linkPath); err != nil {
		t.Fatalf("删除内部 symlink 失败: %v", err)
	}
	if err := os.Symlink(outsideTarget, linkPath); err != nil {
		t.Fatalf("创建越界 symlink 失败: %v", err)
	}
	clock.Add(15 * time.Second)

	ready, err := queue.Check(rule)
	if err != nil {
		t.Fatalf("越界 symlink 稳定性检查不应中断队列: %v", err)
	}
	if len(ready) != 0 {
		t.Fatalf("ready=%v，期望越界 symlink 不进入稳定文件", ready)
	}
	if pending := queue.PendingPaths(rule.ID); len(pending) != 0 {
		t.Fatalf("pending paths=%v，期望越界 symlink 被移出稳定性队列", pending)
	}
}

func writeFileWithMtime(t *testing.T, path string, data []byte, mtime time.Time) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("写入测试文件失败: %v", err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatalf("设置测试文件 mtime 失败: %v", err)
	}
}
