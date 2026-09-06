package helpers

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAcquireInstanceLock(t *testing.T) {
	dir := t.TempDir()
	first, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := AcquireInstanceLock(dir); err == nil {
		other.Close()
		first.Close()
		t.Fatal("同一配置目录被重复锁定")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".qmediasync.lock")); err != nil {
		t.Fatal("释放锁不应删除锁文件")
	}
	next, err := AcquireInstanceLock(dir)
	if err != nil {
		t.Fatalf("关闭文件后锁未释放：%v", err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")
	if file, err := AcquireInstanceLock(missing); err == nil {
		file.Close()
		t.Fatal("锁操作不应创建缺失的配置目录")
	}
}
