package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestListBackupFilesReadsMetadataOnly(t *testing.T) {
	setupBackupTest(t)
	files, err := ListBackupFiles()
	if err != nil || len(files) != 0 {
		t.Fatalf("缺少目录应返回空列表：%+v, %v", files, err)
	}
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups", "temp"); err != nil {
		t.Fatal(err)
	}
	root := BackupDirectory()
	for _, name := range []string{"invalid.zip", "大文件.ZIP", ".backup-publish-hidden.part", "notes.txt", "temp/upload_hidden.zip"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("not a valid archive"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// 超过恢复上限的稀疏文件仍可快速列举；列表不打开归档或据此宣称可恢复。
	if err := os.Truncate(filepath.Join(root, "大文件.ZIP"), MaxArchiveSize+1); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "folder.zip"), 0700); err != nil {
		t.Fatal(err)
	}
	when := time.Unix(1700000000, 0)
	for _, name := range []string{"invalid.zip", "大文件.ZIP"} {
		if err := os.Chtimes(filepath.Join(root, name), when, when); err != nil {
			t.Fatal(err)
		}
	}
	files, err = ListBackupFiles()
	want := []BackupFile{
		{FileName: "invalid.zip", FileSize: int64(len("not a valid archive")), ModifiedAt: when.Unix()},
		{FileName: "大文件.ZIP", FileSize: MaxArchiveSize + 1, ModifiedAt: when.Unix()},
	}
	if err != nil || !reflect.DeepEqual(files, want) {
		t.Fatalf("文件元数据不符：%+v，%v", files, err)
	}
	if got := BackupFileAvailability(filepath.Join(root, "invalid.zip")); got != "available" {
		t.Fatalf("文件存在性不应受内容校验影响：%s", got)
	}
}

func TestBackupFilesRejectPathsAndSymlinks(t *testing.T) {
	setupBackupTest(t)
	if got := BackupFileAvailability(filepath.Join(BackupDirectory(), "unknown.zip")); got != "unavailable" {
		t.Fatalf("备份目录尚不可用时不能断言文件已丢失：%s", got)
	}
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups", "temp"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "..", ".", "../backup.zip", "/backup.zip", "dir/backup.zip", `dir\backup.zip`, `C:\backup.zip`, "a\x00.zip", "a.txt", "a.zip/"} {
		if _, err := ResolveBackupFile(name); !errors.Is(err, ErrBackupFileNameInvalid) {
			t.Errorf("未拒绝无效文件名 %q：%v", name, err)
		}
	}
	root := BackupDirectory()
	filePath := filepath.Join(root, "allowed.zip")
	if err := os.WriteFile(filePath, []byte("archive"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveBackupFile("allowed.zip"); err != nil {
		t.Fatal(err)
	}
	if got := BackupFileAvailability(filepath.Join(root, "missing.zip")); got != "missing" {
		t.Fatalf("不存在的文件状态为 %s", got)
	}
	if got := BackupFileAvailability(""); got != "unavailable" {
		t.Fatalf("空路径状态为 %s", got)
	}
	if got := BackupFileAvailability(filepath.Join(t.TempDir(), "outside.zip")); got != "unavailable" {
		t.Fatalf("外部路径不能宣称已丢失：%s", got)
	}
	outside := filepath.Join(t.TempDir(), "outside.zip")
	if err := os.WriteFile(outside, []byte("external contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.zip")); err != nil {
		t.Skipf("平台不支持符号链接：%v", err)
	}
	if got := BackupFileAvailability(filepath.Join(root, "link.zip")); got != "unavailable" {
		t.Fatalf("符号链接状态为 %s", got)
	}
	files, err := ListBackupFiles()
	if err != nil || len(files) != 1 || files[0].FileName != "allowed.zip" {
		t.Fatalf("列表不能包含符号链接：%+v, %v", files, err)
	}
	// 模拟列表/路径核验后文件被替换：实际下载或恢复打开时再次拒绝链接。
	if err := os.Remove(filePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filePath); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenBackupFile(filePath); err == nil {
		file.Close()
		t.Fatal("打开已被替换的符号链接")
	}
	if err := os.Rename(root, root+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Dir(outside), root); err != nil {
		t.Fatal(err)
	}
	if _, err := ListBackupFiles(); !errors.Is(err, ErrBackupFileUnavailable) {
		t.Fatalf("备份根目录链接未被拒绝：%v", err)
	}
	if got := BackupFileAvailability(filepath.Join(root, "missing.zip")); got != "unavailable" {
		t.Fatalf("目录不可用不能当作文件已丢失：%s", got)
	}
}

func TestUploadedBackupPublishesAfterPreflightAndSurvivesRestore(t *testing.T) {
	for _, scenario := range []string{"completed", "preflight_failure", "maintenance_failure", "transaction_failure"} {
		t.Run(scenario, func(t *testing.T) {
			conn := setupBackupTest(t)
			if err := conn.Create(&backupTestItem{ID: 1, Name: "archive value"}).Error; err != nil {
				t.Fatal(err)
			}
			archive := logicalArchiveForRestore(t, conn)
			content, err := os.ReadFile(archive)
			if err != nil {
				t.Fatal(err)
			}
			if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups", "temp"); err != nil {
				t.Fatal(err)
			}
			tempPath := filepath.Join(BackupDirectory(), "temp", "upload_test.zip")
			if scenario == "preflight_failure" {
				content = []byte("broken archive")
			}
			if err := os.WriteFile(tempPath, content, 0600); err != nil {
				t.Fatal(err)
			}
			if err := conn.Model(&backupTestItem{}).Where("id = 1").UpdateColumn("name", "target value").Error; err != nil {
				t.Fatal(err)
			}
			maintenanceCalls := 0
			beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
				maintenanceCalls++
				files, err := ListBackupFiles()
				if err != nil || len(files) != 1 {
					return nil, errors.New("进入维护前必须有已发布归档")
				}
				if scenario == "maintenance_failure" {
					return nil, errors.New("maintenance unavailable")
				}
				return &restoreMaintenance{database: conn}, nil
			}
			if scenario == "transaction_failure" {
				if err := conn.Callback().Create().Before("gorm:create").Register("test:restore_failure", func(tx *gorm.DB) {
					if tx.Statement.Table == "backup_test_items" {
						tx.AddError(errors.New("injected import failure"))
					}
				}); err != nil {
					t.Fatal(err)
				}
			}
			receipt, err := StartUploadedRestoreWithReceipt(tempPath)
			if err != nil || receipt == "" {
				t.Fatalf("启动上传恢复失败：%v", err)
			}
			result := waitUploadedRestore(t, receipt, tempPath)
			wantStatus := models.BackupStatusFailed
			if scenario == "completed" {
				wantStatus = models.BackupStatusCompleted
			}
			if result.Status != wantStatus {
				t.Fatalf("恢复结果不符：%+v", result)
			}
			files, err := ListBackupFiles()
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "preflight_failure" {
				if len(files) != 0 || maintenanceCalls != 0 {
					t.Fatalf("坏包不应发布或进入维护：%+v, %d", files, maintenanceCalls)
				}
			} else {
				if len(files) != 1 || !strings.HasPrefix(files[0].FileName, "backup_upload_") {
					t.Fatalf("恢复后已发布的上传文件丢失：%+v", files)
				}
				published := filepath.Join(BackupDirectory(), files[0].FileName)
				stored, err := os.ReadFile(published)
				if err != nil || !reflect.DeepEqual(stored, content) {
					t.Fatalf("保存的上传内容改变：%v", err)
				}
				if scenario == "transaction_failure" {
					var item backupTestItem
					if err := conn.First(&item, 1).Error; err != nil || item.Name != "target value" {
						t.Fatalf("失败未回滚目标数据：%+v, %v", item, err)
					}
					conn.Callback().Create().Remove("test:restore_failure")
				}
				beginRestoreMaintenance = func(context.Context) (*restoreMaintenance, error) {
					return &restoreMaintenance{database: conn}, nil
				}
				if err := Restore(published); err != nil {
					t.Fatalf("正式文件不能再次恢复：%v", err)
				}
				var count int64
				if err := conn.Model(&models.BackupRecord{}).Count(&count).Error; err != nil || count != 0 {
					t.Fatalf("上传不应伪造备份执行历史：%d, %v", count, err)
				}
				if err := os.WriteFile(published, []byte("modified archive"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := Restore(published); !errors.Is(err, ErrArchiveInvalid) {
					t.Fatalf("再次恢复必须重新校验所选归档：%v", err)
				}
			}
			for _, pattern := range []string{"backup-restore-*", ".backup-publish-*.part"} {
				if leftovers, _ := filepath.Glob(filepath.Join(BackupDirectory(), pattern)); len(leftovers) != 0 {
					t.Fatalf("恢复临时文件未清理：%v", leftovers)
				}
			}
		})
	}
}

func waitUploadedRestore(t *testing.T, receipt, tempPath string) *BackupOrRestoreResult {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("上传恢复未及时结束或临时上传文件未清理")
		case <-ticker.C:
			result, valid := RestoreResultWithReceipt(receipt)
			if !valid {
				t.Fatal("上传恢复回执失效")
			}
			if _, err := os.Stat(tempPath); !result.IsRunning && errors.Is(err, os.ErrNotExist) {
				return result
			}
		}
	}
}
