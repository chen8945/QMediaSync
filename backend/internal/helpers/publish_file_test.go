package helpers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSyncAndPublishFileNoReplace(t *testing.T) {
	for _, scenario := range []string{"success", "existing_file", "existing_directory", "missing_source", "different_directory"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			source, target := filepath.Join(dir, ".backup.part"), filepath.Join(dir, "backup.zip")
			if scenario != "missing_source" {
				if err := os.WriteFile(source, []byte("complete archive"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "existing_file":
				if err := os.WriteFile(target, []byte("existing archive"), 0600); err != nil {
					t.Fatal(err)
				}
			case "existing_directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			case "different_directory":
				target = filepath.Join(t.TempDir(), "backup.zip")
			}
			err := SyncAndPublishFileNoReplace(source, target)
			if (err == nil) != (scenario == "success") {
				t.Fatalf("publish=%v", err)
			}
			if scenario == "existing_file" && !errors.Is(err, os.ErrExist) {
				t.Fatalf("目标存在的错误链丢失：%v", err)
			}
			got, readErr := os.ReadFile(target)
			switch scenario {
			case "success":
				if readErr != nil || string(got) != "complete archive" {
					t.Fatalf("发布不完整：%q %v", got, readErr)
				}
			case "existing_file":
				if readErr != nil || string(got) != "existing archive" {
					t.Fatalf("覆盖了已有文件：%q %v", got, readErr)
				}
			case "missing_source", "different_directory":
				if !errors.Is(readErr, os.ErrNotExist) {
					t.Fatalf("失败发布留下目标：%v", readErr)
				}
			}
		})
	}
}

func TestSyncAndPublishConcurrentCollision(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "backup.zip")
	const publishers = 12
	results := make(chan string, publishers)
	var wg sync.WaitGroup
	for i := range publishers {
		content := fmt.Sprintf("complete archive %d", i)
		source := filepath.Join(dir, fmt.Sprintf(".backup-%d.part", i))
		if err := os.WriteFile(source, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		wg.Go(func() {
			err := SyncAndPublishFileNoReplace(source, target)
			if err == nil {
				results <- content
			} else if !errors.Is(err, os.ErrExist) {
				t.Errorf("并发发布错误：%v", err)
			}
		})
	}
	wg.Wait()
	close(results)
	if len(results) != 1 {
		t.Fatalf("必须恰好一个发布成功：%d", len(results))
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != <-results {
		t.Fatalf("竞争者覆盖已发布结果：%q %v", got, err)
	}
}

func TestSyncAndPublishRejectsSymbolicLink(t *testing.T) {
	dir := t.TempDir()
	real, source, target := filepath.Join(dir, "real"), filepath.Join(dir, "source"), filepath.Join(dir, "backup.zip")
	if err := os.WriteFile(real, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, source); err != nil {
		t.Skipf("符号链接不可用：%v", err)
	}
	if err := SyncAndPublishFileNoReplace(source, target); err == nil {
		t.Fatal("不能发布符号链接源")
	}
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝后不能产生最终路径：%v", err)
	}
}
