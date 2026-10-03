package directoryupload

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

func TestResolveMetadataSource(t *testing.T) {
	for _, name := range []string{"regular", "source_link", "monitor_link", "outside_link", "missing_source", "missing_monitor", "missing_rule", "deleted_mapped_rule", "empty_monitor", "wrong_account", "wrong_sync_path"} {
		t.Run(name, func(t *testing.T) {
			setupDirectoryUploadServiceTestDB(t)
			monitor := t.TempDir()
			_, rule := createDirectoryUploadRuleForTest(t, monitor)
			file := filepath.Join(monitor, "poster.jpg")
			if err := os.WriteFile(file, []byte("poster"), 0600); err != nil {
				t.Fatal(err)
			}
			task := createCleanupUploadTask(t, rule, file, models.UploadResultMultipartUploaded)
			want := file
			if name == "source_link" || name == "outside_link" {
				if name == "outside_link" {
					want = filepath.Join(t.TempDir(), "outside.jpg")
					if err := os.WriteFile(want, []byte("outside"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				task.LocalFullPath = filepath.Join(monitor, "link.jpg")
				if err := os.Symlink(want, task.LocalFullPath); err != nil {
					t.Fatal(err)
				}
			}
			if name == "monitor_link" {
				link := filepath.Join(t.TempDir(), "monitor")
				if err := os.Symlink(monitor, link); err != nil {
					t.Fatal(err)
				}
				rule.MonitorPath = link
				task.LocalFullPath = filepath.Join(link, "poster.jpg")
			}
			if name == "missing_source" {
				if err := os.Remove(file); err != nil {
					t.Fatal(err)
				}
			}
			if name == "missing_monitor" {
				rule.MonitorPath = filepath.Join(t.TempDir(), "missing")
				task.LocalFullPath = filepath.Join(rule.MonitorPath, "poster.jpg")
			}
			// 映射存在时必须使用原规则，不能靠其他规则绕过失效或范围不匹配。
			if name == "deleted_mapped_rule" || name == "empty_monitor" || name == "wrong_account" || name == "wrong_sync_path" {
				if err := db.Db.Create(&models.DirectoryUploadProcessedFile{RuleId: rule.ID, UploadTaskId: task.ID}).Error; err != nil {
					t.Fatal(err)
				}
			}
			switch name {
			case "empty_monitor":
				rule.MonitorPath = ""
			case "wrong_account":
				rule.AccountId++
			case "wrong_sync_path":
				rule.SyncPathId++
			}
			if err := db.Db.Save(rule).Error; err != nil {
				t.Fatal(err)
			}
			if name == "missing_rule" || name == "deleted_mapped_rule" {
				if err := db.Db.Delete(rule).Error; err != nil {
					t.Fatal(err)
				}
			}
			original := task.LocalFullPath
			got, err := ResolveMetadataSource(task)
			if name == "regular" || name == "source_link" || name == "monitor_link" {
				if err != nil || got != want {
					t.Fatalf("解析源文件 = %q, %v，期望 %q", got, err, want)
				}
			} else if err == nil || got != "" {
				t.Fatalf("不安全或缺失的源文件不应解析成功：%q, %v", got, err)
			}
			if name == "outside_link" && !errors.Is(err, errPathResolvedOutsideMonitor) {
				t.Fatalf("应保留越界原因：%v", err)
			}
			if task.LocalFullPath != original {
				t.Fatalf("不应改写上传任务路径：%q", task.LocalFullPath)
			}
		})
	}
}

func TestResolveMetadataSourceRetargetAndCleanup(t *testing.T) {
	setupDirectoryUploadServiceTestDB(t)
	monitor := t.TempDir()
	_, rule := createDirectoryUploadRuleForTest(t, monitor)
	rule.DeleteSourceAfterSuccess = true
	if err := db.Db.Save(rule).Error; err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(monitor, "first.jpg")
	second := filepath.Join(monitor, "second.jpg")
	for _, file := range []string{first, second} {
		if err := os.WriteFile(file, []byte("poster"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(monitor, "link.jpg")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	task := createCleanupUploadTask(t, rule, link, models.UploadResultMultipartUploaded)
	before, err := ResolveMetadataSource(task)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(second, link); err != nil {
		t.Fatal(err)
	}
	after, err := ResolveMetadataSource(task)
	if err != nil || before != first || after != second || before == after {
		t.Fatalf("重新解析应识别链接重定向：before=%q after=%q err=%v", before, after, err)
	}
	// 回到上传时的文件后，真实清理入口只删除原链接。
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	createCleanupStrmTask(t, task.ID, models.StrmGenerationStatusCompleted)
	if err := CleanupSourceAfterStrmSuccess(task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原链接未被清理：%v", err)
	}
	for _, file := range []string{first, second} {
		content, err := os.ReadFile(file)
		if err != nil || string(content) != "poster" {
			t.Fatalf("链接目标应保留：%s content=%q err=%v", file, content, err)
		}
	}
}
