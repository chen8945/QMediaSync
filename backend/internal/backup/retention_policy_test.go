package backup

import (
	"archive/zip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

func TestBackupRetentionPolicy(t *testing.T) {
	testBackupRetentionPolicy(t, setupBackupTest)
}

func testBackupRetentionPolicy(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, tc := range []struct {
		name      string
		retention int
		maxCount  int
		ages      []time.Duration
		keep      []bool
	}{
		{
			name: "same_second_keeps_highest_ids_and_reserves_slot", maxCount: 3,
			ages: []time.Duration{time.Hour, time.Hour, time.Hour, time.Hour},
			keep: []bool{false, false, true, true},
		},
		{
			name: "creation_time_precedes_id", maxCount: 2,
			ages: []time.Duration{time.Hour, 2 * time.Hour}, keep: []bool{true, false},
		},
		{
			name: "maximum_one_reserves_only_slot", maxCount: 1,
			ages: []time.Duration{time.Hour, 2 * time.Hour}, keep: []bool{false, false},
		},
		{
			name: "zero_count_is_unlimited", retention: 1,
			ages: []time.Duration{time.Hour, 2 * time.Hour, 48 * time.Hour},
			keep: []bool{true, true, false},
		},
		{
			name: "both_zero_disables_retention",
			ages: []time.Duration{time.Hour, 48 * time.Hour, 365 * 24 * time.Hour},
			keep: []bool{true, true, true},
		},
		{
			name: "age_expires_records_below_count_limit", retention: 1, maxCount: 3,
			ages: []time.Duration{time.Hour, 48 * time.Hour, 49 * time.Hour},
			keep: []bool{true, false, false},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := setup(t)
			service := configureBackupRetention(t, tc.retention, tc.maxCount)
			now := time.Now()
			var records []models.BackupRecord
			for i, age := range tc.ages {
				records = append(records, seedRetentionRecord(t, conn, fmt.Sprintf("managed-%d.zip", i), models.BackupStatusCompleted, now.Add(-age).Unix()))
			}
			// 任务结果不是 completed 的记录不纳入保留计数，也不按文件时间猜测结果。
			var protected []models.BackupRecord
			for _, status := range []string{
				models.BackupStatusPending, models.BackupStatusRunning, models.BackupStatusFailed,
				models.BackupStatusCancelled, models.BackupStatusTimeout, models.BackupStatusUnconfirmed,
			} {
				protected = append(protected, seedRetentionRecord(t, conn, status+".zip", status, now.Add(-365*24*time.Hour).Unix()))
			}
			var untracked []string
			for _, name := range []string{"copied.zip", "uploaded.zip"} {
				path := filepath.Join(helpers.ConfigDir, "backups", name)
				if err := os.WriteFile(path, []byte("untracked archive"), 0600); err != nil {
					t.Fatal(err)
				}
				old := now.Add(-365 * 24 * time.Hour)
				if err := os.Chtimes(path, old, old); err != nil {
					t.Fatal(err)
				}
				untracked = append(untracked, path)
			}

			if err := service.CleanupOldBackupsBeforeBackup(); err != nil {
				t.Fatal(err)
			}
			for i, record := range records {
				assertRetentionRecord(t, conn, record, tc.keep[i])
			}
			for _, record := range protected {
				assertRetentionRecord(t, conn, record, true)
			}
			for _, path := range untracked {
				content, err := os.ReadFile(path)
				if err != nil || string(content) != "untracked archive" {
					t.Fatalf("未纳管文件被清理或改写：%s，%v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestBackupRetentionConcurrentDeletion(t *testing.T) {
	testBackupRetentionConcurrentDeletion(t, setupBackupTest)
}

func testBackupRetentionConcurrentDeletion(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, tc := range []struct {
		name       string
		backupType string
		retention  int
		maxCount   int
	}{
		{name: "manual_count", backupType: models.BackupTypeManual, maxCount: 1},
		{name: "auto_age", backupType: models.BackupTypeAuto, retention: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conn := setup(t)
			service := configureBackupRetention(t, tc.retention, tc.maxCount)
			older := seedRetentionRecord(t, conn, "older.zip", models.BackupStatusCompleted, 100)
			candidate := seedRetentionRecord(t, conn, "candidate.zip", models.BackupStatusCompleted, 200)
			deleted := false
			const callbackName = "test:retention_concurrent_delete"
			if err := conn.Callback().Query().After("gorm:query").Register(callbackName, func(tx *gorm.DB) {
				if _, ok := tx.Statement.Dest.(*[]models.BackupRecord); !ok || tx.Error != nil || deleted {
					return
				}
				// 列表已读取、逐条清理尚未开始时，确定性模拟用户手动删除第一个候选。
				deleted = true
				if !IsRunning() {
					t.Fatal("手动删除旧记录时，新备份应仍在运行")
				}
				if err := service.DeleteBackup(candidate.ID); err != nil {
					t.Fatalf("手动删除旧记录失败：%v", err)
				}
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Callback().Query().Remove(callbackName); err != nil {
					t.Error(err)
				}
			})

			const reason = "保留策略并发删除回归"
			if err := Backup(tc.backupType, reason); err != nil {
				t.Fatalf("并发删除旧记录不应中止新备份：%v", err)
			}
			if !deleted {
				t.Fatal("未触发清理列表读取后的手动删除")
			}
			assertRetentionRecord(t, conn, candidate, false)
			assertRetentionRecord(t, conn, older, false)
			if err := service.DeleteBackup(candidate.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
				t.Fatalf("直接删除不存在记录仍应返回 ErrRecordNotFound：%v", err)
			}
			var current models.BackupRecord
			if err := conn.Where("created_reason = ?", reason).First(&current).Error; err != nil {
				t.Fatal(err)
			}
			if current.Status != models.BackupStatusCompleted || current.BackupType != tc.backupType ||
				current.FailureReason != "" || current.FilePath == "" || current.FileSize <= 0 {
				t.Fatalf("新备份未保存完成历史和归档信息：%+v", current)
			}
			archive, err := zip.OpenReader(current.FilePath)
			if err != nil {
				t.Fatalf("新备份 ZIP 不可读取：%v", err)
			}
			if err := archive.Close(); err != nil {
				t.Fatal(err)
			}
			if result := GetRunningResult(); result.IsRunning || result.Status != models.BackupStatusCompleted {
				t.Fatalf("新备份未发布完成状态：%+v", result)
			}
		})
	}
}

func TestBackupRetentionErrors(t *testing.T) {
	testBackupRetentionErrors(t, setupBackupTest)
}

func testBackupRetentionErrors(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, stage := range []string{"query", "record_delete", "outside_record_delete", "file_delete"} {
		t.Run(stage, func(t *testing.T) {
			conn := setup(t)
			service := configureBackupRetention(t, 0, 1)
			older := seedRetentionRecord(t, conn, "older.zip", models.BackupStatusCompleted, 100)
			newer := seedRetentionRecord(t, conn, "newer.zip", models.BackupStatusCompleted, 200)
			currentPath := newer.FilePath
			if stage == "outside_record_delete" {
				newer.FilePath = filepath.Join(t.TempDir(), "newer.zip")
				if err := os.WriteFile(newer.FilePath, []byte("archive newer.zip"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := conn.Model(&newer).Update("file_path", newer.FilePath).Error; err != nil {
					t.Fatal(err)
				}
			}
			injected := errors.New("retention storage unavailable")
			switch stage {
			case "query":
				if err := conn.Callback().Query().Before("gorm:query").Register("test:retention_query", func(tx *gorm.DB) {
					tx.AddError(injected)
				}); err != nil {
					t.Fatal(err)
				}
			case "record_delete", "outside_record_delete":
				if err := conn.Callback().Delete().Before("gorm:delete").Register("test:retention_delete", func(tx *gorm.DB) {
					tx.AddError(injected)
				}); err != nil {
					t.Fatal(err)
				}
			case "file_delete":
				if err := os.Remove(newer.FilePath); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(newer.FilePath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			err := service.CleanupOldBackupsBeforeBackup()
			if stage == "query" {
				if removeErr := conn.Callback().Query().Remove("test:retention_query"); removeErr != nil {
					t.Fatal(removeErr)
				}
			}
			if err == nil || (stage != "file_delete" && !errors.Is(err, injected)) {
				t.Fatalf("清理未传播 %s 失败：%v", stage, err)
			}
			for _, record := range []models.BackupRecord{older, newer} {
				var got models.BackupRecord
				if err := conn.First(&got, record.ID).Error; err != nil || !reflect.DeepEqual(got, record) {
					t.Fatalf("失败后应保留历史记录：got=%+v，want=%+v，err=%v", got, record, err)
				}
			}
			// 第一个候选失败后必须停止，不能继续删除后续仍可用的候选。
			assertRetentionRecord(t, conn, older, true)
			if stage == "query" {
				assertRetentionRecord(t, conn, newer, true)
			}
			if stage == "outside_record_delete" {
				assertRetentionRecord(t, conn, newer, true)
				if content, err := os.ReadFile(currentPath); err != nil || string(content) != "archive newer.zip" {
					t.Fatalf("数据库删除失败时修改了当前目录同名文件：%v", err)
				}
			}
			if stage == "file_delete" {
				if info, err := os.Stat(newer.FilePath); err != nil || !info.IsDir() {
					t.Fatalf("不应递归删除同名目录：%v", err)
				}
			}
		})
	}
}

func TestBackupDeleteSafety(t *testing.T) {
	testBackupDeleteSafety(t, setupBackupTest)
}

func testBackupDeleteSafety(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	for _, scenario := range []string{
		"ordinary", "missing", "blank", "outside", "outside_missing", "relative_path", "windows_path",
		"windows_device_name", "windows_nested_device_name", "windows_colon_name",
		"nested", "non_zip", "directory", "symlink", "directory_symlink",
	} {
		t.Run(scenario, func(t *testing.T) {
			windowsInvalidPath := scenario == "windows_device_name" ||
				scenario == "windows_nested_device_name" || scenario == "windows_colon_name"
			if runtime.GOOS != "windows" && windowsInvalidPath {
				t.Skip("仅 Windows 对设备名和冒号路径执行非本地路径校验")
			}
			if runtime.GOOS == "windows" && (scenario == "symlink" || scenario == "directory_symlink") {
				t.Skip("Windows 符号链接需要额外系统权限")
			}
			conn := setup(t)
			service := configureBackupRetention(t, 0, 0)
			backupDir := filepath.Join(helpers.ConfigDir, "backups")
			currentPath := filepath.Join(backupDir, "archive.zip")
			path := currentPath
			protectedPath := path
			recordOnly := scenario == "outside" || scenario == "outside_missing" ||
				scenario == "relative_path" || scenario == "windows_path"
			switch scenario {
			case "blank":
				path = ""
			case "outside":
				path = filepath.Join(t.TempDir(), "archive.zip")
				protectedPath = path
				if err := os.WriteFile(currentPath, []byte("keep original"), 0600); err != nil {
					t.Fatal(err)
				}
			case "outside_missing":
				path = filepath.Join(t.TempDir(), "archive.zip")
			case "relative_path":
				path = filepath.Join("old-config", "backups", "archive.zip")
			case "windows_path":
				path = `Z:\old-config\backups\archive.zip`
				if strings.EqualFold(filepath.VolumeName(backupDir), "Z:") {
					path = `Y:\old-config\backups\archive.zip`
				}
			case "windows_device_name":
				path = filepath.Join(backupDir, "NUL")
			case "windows_nested_device_name":
				path = filepath.Join(backupDir, "nested", "CON")
			case "windows_colon_name":
				path = filepath.Join(backupDir, "archive:stream.zip")
			case "nested":
				if err := os.Mkdir(filepath.Join(backupDir, "nested"), 0700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(backupDir, "nested", "archive.zip")
				protectedPath = path
			case "non_zip":
				path = filepath.Join(backupDir, "archive.txt")
				protectedPath = path
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
				protectedPath = filepath.Join(path, "keep.txt")
			case "symlink":
				protectedPath = filepath.Join(t.TempDir(), "archive.zip")
				if err := os.Symlink(protectedPath, path); err != nil {
					t.Fatal(err)
				}
			case "directory_symlink":
				outside := t.TempDir()
				protectedPath = filepath.Join(outside, "archive.zip")
				if err := os.Remove(backupDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, backupDir); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "relative_path" || scenario == "windows_path" {
				// 分别覆盖绝对 / 相对路径不兼容和 Windows 跨卷（其他平台为旧 Windows 路径）。
				if _, err := filepath.Rel(backupDir, path); err == nil {
					t.Fatal("测试路径应无法映射到当前备份目录")
				}
			}
			if windowsInvalidPath {
				// 只写普通保护文件，设备名和流路径仅作为历史记录输入。
				name, err := filepath.Rel(backupDir, path)
				if err != nil || filepath.IsLocal(name) {
					t.Fatalf("测试路径应位于当前目录内但不符合本地路径规则：%q，%v", name, err)
				}
			}
			if scenario != "missing" && scenario != "blank" {
				if err := os.WriteFile(protectedPath, []byte("keep original"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			record := models.BackupRecord{Status: models.BackupStatusCompleted, FilePath: path}
			if err := conn.Create(&record).Error; err != nil {
				t.Fatal(err)
			}
			wantSuccess := scenario == "ordinary" || scenario == "missing" || scenario == "blank" || recordOnly
			err := service.DeleteBackup(record.ID)
			if (err == nil) != wantSuccess {
				t.Fatalf("DeleteBackup() = %v，场景 %s", err, scenario)
			}
			var got models.BackupRecord
			err = conn.First(&got, record.ID).Error
			if wantSuccess {
				if !errors.Is(err, gorm.ErrRecordNotFound) {
					t.Fatalf("记录未删除：%v", err)
				}
				if path != "" && !recordOnly {
					if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("文件未删除：%v", err)
					}
				}
			} else {
				if err != nil || !reflect.DeepEqual(got, record) {
					t.Fatalf("拒绝删除时改写了记录：%+v，%v", got, err)
				}
			}
			if !wantSuccess || recordOnly {
				if content, err := os.ReadFile(protectedPath); err != nil || string(content) != "keep original" {
					t.Fatalf("修改了受保护文件：%v", err)
				}
			}
			if recordOnly {
				if content, err := os.ReadFile(currentPath); err != nil || string(content) != "keep original" {
					t.Fatalf("清理旧路径记录时修改了当前目录同名文件：%v", err)
				}
			}
		})
	}
}

func TestBackupReconcileInterrupted(t *testing.T) {
	testBackupReconcileInterrupted(t, setupBackupTest)
}

func testBackupReconcileInterrupted(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	conn := setup(t)
	var originals []models.BackupRecord
	for i, status := range []string{
		models.BackupStatusPending, models.BackupStatusRunning, models.BackupStatusCompleted,
		models.BackupStatusFailed, models.BackupStatusCancelled, models.BackupStatusTimeout,
		models.BackupStatusUnconfirmed,
	} {
		record := models.BackupRecord{
			BaseModel: models.BaseModel{CreatedAt: 100, UpdatedAt: 101},
			Status:    status, FilePath: filepath.Join(helpers.ConfigDir, "backups", status+".zip"),
			CompletedAt: int64(i) * 200, BackupDuration: 30, FailureReason: "existing diagnostic",
		}
		if err := conn.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		originals = append(originals, record)
	}
	if err := models.ReconcileInterruptedBackups(); err != nil {
		t.Fatal(err)
	}
	var reconciled []models.BackupRecord
	if err := conn.Order("id ASC").Find(&reconciled).Error; err != nil {
		t.Fatal(err)
	}
	if len(reconciled) != len(originals) {
		t.Fatalf("归一化改变了记录数量：%d", len(reconciled))
	}
	for i, original := range originals {
		got := reconciled[i]
		want := original
		if original.Status == models.BackupStatusPending || original.Status == models.BackupStatusRunning {
			if got.Status != models.BackupStatusUnconfirmed || got.FailureReason == "" || got.FailureReason == original.FailureReason {
				t.Fatalf("中断记录没有保存未确认状态和原因：%+v", got)
			}
			want.Status, want.FailureReason, want.UpdatedAt = got.Status, got.FailureReason, got.UpdatedAt
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("归一化修改了结果、完成时间或其他历史字段：got=%+v，want=%+v", got, want)
		}
	}
	if err := models.ReconcileInterruptedBackups(); err != nil {
		t.Fatal(err)
	}
	var repeated []models.BackupRecord
	if err := conn.Order("id ASC").Find(&repeated).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(repeated, reconciled) {
		t.Fatal("重复启动改写了已收敛的记录")
	}
}

func TestBackupReconcileFailure(t *testing.T) {
	testBackupReconcileFailure(t, setupBackupTest)
}

func testBackupReconcileFailure(t *testing.T, setup func(*testing.T) *gorm.DB) {
	t.Helper()
	conn := setup(t)
	record := models.BackupRecord{Status: models.BackupStatusRunning}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	injected := errors.New("backup history write unavailable")
	if err := conn.Callback().Update().Before("gorm:update").Register("test:reconcile_failure", func(tx *gorm.DB) {
		tx.AddError(injected)
	}); err != nil {
		t.Fatal(err)
	}
	if err := models.ReconcileInterruptedBackups(); !errors.Is(err, injected) {
		t.Fatalf("归一化未传播数据库错误：%v", err)
	}
	var got models.BackupRecord
	if err := conn.First(&got, record.ID).Error; err != nil || !reflect.DeepEqual(got, record) {
		t.Fatalf("失败后历史被改写：got=%+v，want=%+v，err=%v", got, record, err)
	}
}

func configureBackupRetention(t *testing.T, retention, maxCount int) *models.BackupService {
	t.Helper()
	service := models.GetBackupService()
	config := *service.GetBackupConfig()
	config.BackupRetention, config.BackupMaxCount = retention, maxCount
	if err := service.UpdateBackupConfig(&config); err != nil {
		t.Fatal(err)
	}
	return service
}

func seedRetentionRecord(t *testing.T, conn *gorm.DB, name, status string, createdAt int64) models.BackupRecord {
	t.Helper()
	record := models.BackupRecord{
		BaseModel: models.BaseModel{CreatedAt: createdAt}, Status: status,
		FilePath: filepath.Join(helpers.ConfigDir, "backups", name),
	}
	if err := conn.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(record.FilePath, []byte("archive "+name), 0600); err != nil {
		t.Fatal(err)
	}
	return record
}

func assertRetentionRecord(t *testing.T, conn *gorm.DB, record models.BackupRecord, wantPresent bool) {
	t.Helper()
	var got models.BackupRecord
	err := conn.First(&got, record.ID).Error
	if wantPresent {
		if err != nil || !reflect.DeepEqual(got, record) {
			t.Fatalf("保留记录不符：got=%+v，want=%+v，err=%v", got, record, err)
		}
		if content, err := os.ReadFile(record.FilePath); err != nil || string(content) != "archive "+filepath.Base(record.FilePath) {
			t.Fatalf("保留文件不符：%s，%v", filepath.Base(record.FilePath), err)
		}
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("应清理记录 %d：%v", record.ID, err)
	}
	if _, err := os.Stat(record.FilePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("应清理文件 %s：%v", filepath.Base(record.FilePath), err)
	}
}
