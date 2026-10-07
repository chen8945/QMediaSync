package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanupTemporaryFilesPreservesUnownedData(t *testing.T) {
	config := t.TempDir()
	removed := []string{
		"backups/backup-export-123/User.json", "backups/backup-restore-456/User.json",
		"backups/.backup-publish-789.part", "backups/temp/upload_123.zip",
	}
	preserved := []string{
		"encryption.key", "config.yaml", "backups/backup_manual_20200101.zip", "backups/migrate.zip",
		"backups/123/User.json", "backups/unrelated/file", "backups/other.part",
		"backups/temp/keep.zip", "backups/temp/upload_keep.txt", "backups/temp/upload_directory.zip/keep",
	}
	for _, name := range append(append([]string{}, removed...), preserved...) {
		path := filepath.Join(config, name)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("content"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := CleanupTemporaryFiles(config); err != nil {
		t.Fatal(err)
	}
	for _, name := range removed {
		if _, err := os.Stat(filepath.Join(config, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("未清理临时项 %s：%v", name, err)
		}
	}
	for _, name := range preserved {
		if content, err := os.ReadFile(filepath.Join(config, name)); err != nil || string(content) != "content" {
			t.Errorf("修改了非受管临时项 %s：%q %v", name, content, err)
		}
	}
	if err := CleanupTemporaryFiles(config); err != nil {
		t.Fatalf("重复清理失败：%v", err)
	}
	if err := CleanupTemporaryFiles(t.TempDir()); err != nil {
		t.Fatalf("无备份目录应直接返回：%v", err)
	}
}

func TestCleanupTemporaryFilesDoesNotFollowSymlinks(t *testing.T) {
	for _, linkName := range []string{"backups", "backups/temp", "backups/backup-export-link", "backups/.backup-publish-link.part", "backups/temp/upload_link.zip", "backups/backup-restore-123/inside"} {
		t.Run(linkName, func(t *testing.T) {
			config, outside := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(outside, "upload_keep.zip"), []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(config, linkName)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Skipf("符号链接不可用：%v", err)
			}
			err := CleanupTemporaryFiles(config)
			if linkName == "backups/backup-restore-123/inside" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				if err == nil {
					t.Fatal("受管入口符号链接必须拒绝")
				}
				if _, err := os.Lstat(path); err != nil {
					t.Fatalf("拒绝后应保留链接：%v", err)
				}
			}
			if content, err := os.ReadFile(filepath.Join(outside, "upload_keep.zip")); err != nil || string(content) != "keep" {
				t.Fatalf("不应访问符号链接目标：%q %v", content, err)
			}
		})
	}
}
