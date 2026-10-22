package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
)

func TestAppWebhookStartup(t *testing.T) {
	const childEnv = "QMS_TEST_WEBHOOK_STARTUP"
	mode := os.Getenv(childEnv)
	if mode == "" {
		for _, mode := range []string{"recovery_failure", "backup_reconciliation_failure", "success"} {
			t.Run(mode, func(t *testing.T) {
				// 最终停机不可在同一进程恢复，使用独立进程验证真实初始化和退出。
				ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
				defer cancel()
				cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAppWebhookStartup$", "-test.count=1")
				cmd.Env = append(os.Environ(), childEnv+"="+mode)
				output, err := cmd.CombinedOutput()
				if ctx.Err() != nil {
					t.Fatalf("startup did not finish: %v\n%s", ctx.Err(), output)
				}
				if mode != "recovery_failure" {
					if err != nil {
						t.Fatalf("successful startup failed: %v\n%s", err, output)
					}
					return
				}
				if exitErr, ok := errors.AsType[*exec.ExitError](err); !ok || exitErr.ExitCode() == 0 {
					t.Fatalf("failed startup did not exit nonzero: %v\n%s", err, output)
				}
				for _, message := range []string{
					"startup recovery rejected", "panic: 初始化环境失败", "startup resources cleaned",
				} {
					if !strings.Contains(string(output), message) {
						t.Fatalf("startup did not report %q:\n%s", message, output)
					}
				}
			})
		}
		return
	}

	rootDir := t.TempDir()
	t.Setenv("TRIM_APPDEST", rootDir)
	t.Setenv("TRIM_PKGETC", "")
	t.Setenv("TRIM_DATA_SHARE_PATHS", "")
	t.Setenv("LOCALAPPDATA", t.TempDir())
	helpers.RootDir = rootDir
	helpers.ConfigDir = filepath.Join(rootDir, "config")
	if err := os.MkdirAll(helpers.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := helpers.MakeDefaultConfig()
	config.Db = helpers.ConfigDb{Engine: helpers.DbEngineSqlite, SqliteFile: "startup.db"}
	config.HttpHost = "127.0.0.1:0"
	helpers.GlobalConfig = *config
	if err := helpers.SaveConfig(config); err != nil {
		t.Fatal(err)
	}
	helpers.AppLogger = &helpers.QLogger{Logger: log.New(io.Discard, "", 0)}
	if err := (&App{}).StartDatabase(); err != nil {
		t.Fatal(err)
	}
	record := models.EmbyWebhookRecord{
		Status: models.EmbyWebhookRunning, ClaimToken: "previous-process", Attempts: 2,
		NextAttemptAt: time.Now().Add(time.Hour).Unix(),
	}
	if err := db.Db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	interruptedBackup := models.BackupRecord{Status: models.BackupStatusRunning}
	if err := db.Db.Create(&interruptedBackup).Error; err != nil {
		t.Fatal(err)
	}
	if mode == "backup_reconciliation_failure" {
		if err := db.Db.Exec(`CREATE TRIGGER reject_backup_reconciliation
			BEFORE UPDATE ON backup_record
			BEGIN SELECT RAISE(FAIL, 'backup reconciliation rejected'); END`).Error; err != nil {
			t.Fatal(err)
		}
	}
	if mode == "recovery_failure" {
		// 迁移仍可完成，只有恢复遗留 running 工作时才触发真实 SQL 错误。
		if err := db.Db.Exec(`CREATE TRIGGER reject_webhook_recovery
			BEFORE UPDATE ON emby_webhook_records
			BEGIN SELECT RAISE(FAIL, 'startup recovery rejected'); END`).Error; err != nil {
			t.Fatal(err)
		}
	}
	fixtureDB, err := db.Db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := fixtureDB.Close(); err != nil {
		t.Fatal(err)
	}
	db.Db = nil
	t.Cleanup(func() {
		if db.Db != nil {
			conn, err := db.Db.DB()
			if err != nil {
				t.Error(err)
			} else if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
	})

	if mode == "backup_reconciliation_failure" {
		if initEnv() {
			QMSApp.Stop()
			t.Fatal("backup reconciliation failure accepted application initialization")
		}
		defer instanceLock.Close()
		defer helpers.CloseLogger()
		if models.GlobalDownloadQueue != nil || models.GlobalUploadQueue != nil {
			t.Fatal("backup reconciliation failure started background queues")
		}
		if QMSApp == nil || QMSApp.httpServer != nil || QMSApp.httpsServer != nil {
			t.Fatal("backup reconciliation failure entered HTTP serving")
		}
		return
	}
	if mode == "recovery_failure" {
		defer func() {
			if QMSApp == nil || QMSApp.httpServer != nil || QMSApp.httpsServer != nil {
				t.Error("failed startup entered HTTP serving")
				return
			}
			if models.GlobalDownloadQueue == nil || models.GlobalDownloadQueue.IsRunning() ||
				models.GlobalUploadQueue == nil || models.GlobalUploadQueue.IsRunning() {
				t.Error("failed startup left initialized queues running")
				return
			}
			if !realtime.GlobalLifecycle.IsStopped() || helpers.AppLogger.Writer() != io.Discard {
				t.Error("failed startup skipped application cleanup")
				return
			}
			lock, err := helpers.AcquireInstanceLock(helpers.ConfigDir)
			if err != nil {
				t.Errorf("failed startup retained the instance lock: %v", err)
				return
			}
			if err := lock.Close(); err != nil {
				t.Error(err)
				return
			}
			fmt.Println("startup resources cleaned")
		}()
		flag.CommandLine = flag.NewFlagSet("startup", flag.ContinueOnError)
		os.Args = []string{"qmediasync-startup-test"}
		main()
		t.Fatal("failed startup returned without a nonzero exit")
	}

	if !initEnv() {
		t.Fatal("successful recovery rejected application initialization")
	}
	defer QMSApp.Stop()
	defer instanceLock.Close()
	if !models.GlobalDownloadQueue.IsRunning() || !models.GlobalUploadQueue.IsRunning() || realtime.GlobalLifecycle.IsStopped() {
		t.Fatal("successful startup stopped background resources")
	}
	var recovered models.EmbyWebhookRecord
	if err := db.Db.First(&recovered, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recovered.Status != models.EmbyWebhookRetry || recovered.ClaimToken != "" || recovered.Attempts != record.Attempts {
		t.Fatalf("startup did not recover persisted work: %+v", recovered)
	}
	var recoveredBackup models.BackupRecord
	if err := db.Db.First(&recoveredBackup, interruptedBackup.ID).Error; err != nil {
		t.Fatal(err)
	}
	if recoveredBackup.Status != models.BackupStatusUnconfirmed || recoveredBackup.CompletedAt != 0 {
		t.Fatalf("startup did not preserve uncertain backup outcome: %+v", recoveredBackup)
	}
}

func TestFnOSConfigDirectoryPreservation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fnOS 配置路径仅用于 Linux")
	}
	oldRoot, oldDir, oldLock, oldConfig := helpers.RootDir, helpers.ConfigDir, instanceLock, helpers.GlobalConfig
	t.Cleanup(func() {
		helpers.RootDir, helpers.ConfigDir, instanceLock, helpers.GlobalConfig = oldRoot, oldDir, oldLock, oldConfig
	})
	for _, scenario := range []string{"fresh", "migrate yaml", "migrate yml", "shared yaml", "shared yml", "conflicting key"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			src, share := filepath.Join(root, "old"), filepath.Join(root, "share")
			dst := filepath.Join(share, "config")
			for _, dir := range []string{src, dst} {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(dir, name, content string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			name := "config.yaml"
			if strings.Contains(scenario, "yml") {
				name = "config.yml"
			}
			if scenario != "fresh" {
				write(src, name, "db:\n    engine: sqlite\n    sqliteFile: existing.db\n")
				write(src, "encryption.key", "old key")
			}
			if strings.HasPrefix(scenario, "shared") {
				write(dst, name, "db:\n    engine: sqlite\n    sqliteFile: shared.db\n")
				write(dst, "encryption.key", "shared key")
			}
			if scenario == "conflicting key" {
				write(dst, "encryption.key", "shared key")
			}
			t.Setenv("TRIM_PKGETC", src)
			t.Setenv("TRIM_DATA_SHARE_PATHS", share)
			helpers.RootDir = root
			instanceLock = nil
			err := getDataAndConfigDir()
			if instanceLock != nil {
				defer instanceLock.Close()
			}
			if scenario == "conflicting key" {
				if err == nil {
					t.Fatal("must reject merging partial instances")
				}
				if key, _ := os.ReadFile(filepath.Join(dst, "encryption.key")); string(key) != "shared key" {
					t.Fatal("shared key overwritten")
				}
				if _, err := os.Stat(filepath.Join(src, name)); err != nil {
					t.Fatal("source changed on conflict")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Lstat(filepath.Join(dst, fnOSConfigMigrationPendingFile)); !os.IsNotExist(err) {
				t.Fatalf("successful startup left a migration marker: %v", err)
			}
			if helpers.ConfigDir != dst {
				t.Fatalf("config dir=%s", helpers.ConfigDir)
			}
			if helpers.HasConfigFile() != (scenario != "fresh") {
				t.Fatal("first-run config detection changed")
			}
			if strings.HasPrefix(scenario, "migrate") {
				if err := helpers.LoadExistingConfig(); err != nil {
					t.Fatal(err)
				}
				if helpers.GlobalConfig.Db.SqliteFile != "existing.db" {
					t.Fatal("existing configuration changed")
				}
				if _, err := os.Stat(filepath.Join(src, name)); !os.IsNotExist(err) {
					t.Fatal("old file not migrated")
				}
			}
			if strings.HasPrefix(scenario, "shared") {
				if err := helpers.LoadExistingConfig(); err != nil {
					t.Fatal(err)
				}
				if helpers.GlobalConfig.Db.SqliteFile != "shared.db" {
					t.Fatal("shared configuration overwritten")
				}
				if key, _ := os.ReadFile(filepath.Join(dst, "encryption.key")); string(key) != "shared key" {
					t.Fatal("shared key overwritten")
				}
				if key, _ := os.ReadFile(filepath.Join(src, "encryption.key")); string(key) != "old key" {
					t.Fatal("old key removed")
				}
			}
		})
	}
}

func TestFnOSConfigMigrationSourceLockRetry(t *testing.T) {
	root := t.TempDir()
	src, dst := filepath.Join(root, "old"), filepath.Join(root, "shared")
	for _, dir := range []string{src, dst} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{
		"config.yaml":    "db:\n    engine: sqlite\n    sqliteFile: existing.db\n",
		"encryption.key": "original key",
		"existing.db":    "original database",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	targetLock, err := helpers.AcquireInstanceLock(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer targetLock.Close()
	sourceLock, err := helpers.AcquireInstanceLock(src)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if sourceLock != nil {
			sourceLock.Close()
		}
	})
	before := make(map[string]os.FileInfo)
	for _, dir := range []string{src, dst} {
		before[dir], err = os.Stat(filepath.Join(dir, ".qmediasync.lock"))
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateFnOSConfig(src, dst); err == nil || !strings.Contains(err.Error(), "配置目录正被") {
		t.Fatalf("migration did not reject a locked source: %v", err)
	}
	for name, want := range files {
		if content, err := os.ReadFile(filepath.Join(src, name)); err != nil || string(content) != want {
			t.Fatalf("locked migration changed source %s: %q, %v", name, content, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, fnOSConfigMigrationPendingFile)); !os.IsNotExist(err) {
		t.Errorf("source lock conflict left a migration marker: %v", err)
	}
	if err := sourceLock.Close(); err != nil {
		t.Fatal(err)
	}
	sourceLock = nil
	if err := migrateFnOSConfig(src, dst); err != nil {
		t.Fatalf("retry after releasing the source lock failed: %v", err)
	}
	for name, want := range files {
		if content, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(content) != want {
			t.Errorf("retry did not preserve %s: %q, %v", name, content, err)
		}
		if _, err := os.Stat(filepath.Join(src, name)); !os.IsNotExist(err) {
			t.Errorf("retry did not move %s: %v", name, err)
		}
	}
	if _, err := os.Lstat(filepath.Join(dst, fnOSConfigMigrationPendingFile)); !os.IsNotExist(err) {
		t.Errorf("successful retry left a migration marker: %v", err)
	}
	for dir, info := range before {
		if after, err := os.Stat(filepath.Join(dir, ".qmediasync.lock")); err != nil || !os.SameFile(info, after) {
			t.Errorf("migration replaced the instance lock in %s: %v", dir, err)
		}
	}
	sourceLock, err = helpers.AcquireInstanceLock(src)
	if err != nil {
		t.Fatalf("migration retained the source lock: %v", err)
	}
}

func TestFnOSConfigIncompleteMigration(t *testing.T) {
	for _, scenario := range []string{"missing source", "empty source", "source lock file only", "all files moved"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			src, dst := filepath.Join(root, "old"), filepath.Join(root, "shared")
			if err := os.MkdirAll(dst, 0o700); err != nil {
				t.Fatal(err)
			}
			if scenario != "missing source" {
				if err := os.MkdirAll(src, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "source lock file only" {
				if err := os.WriteFile(filepath.Join(src, ".qmediasync.lock"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{
				fnOSConfigMigrationPendingFile: src + "\n",
				"config.yaml":                  "db:\n    engine: sqlite\n    sqliteFile: existing.db\n",
			}
			if scenario == "all files moved" {
				files["encryption.key"] = "original key"
				files["existing.db"] = "original database"
			}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dst, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := migrateFnOSConfig(src, dst); err == nil || !strings.Contains(err.Error(), "迁移尚未完成") {
				t.Fatalf("unfinished migration was not rejected: %v", err)
			}
			for name, want := range files {
				if content, err := os.ReadFile(filepath.Join(dst, name)); err != nil || string(content) != want {
					t.Errorf("unfinished migration changed %s: %q, %v", name, content, err)
				}
			}
		})
	}
}

func TestFnOSConfigMigrationFailureRetry(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("跨文件系统迁移故障仅在 Linux 验证")
	}
	src, err := os.MkdirTemp("/dev/shm", "qms-migration-")
	if err != nil {
		t.Skipf("没有可用的第二文件系统：%v", err)
	}
	t.Cleanup(func() { os.RemoveAll(src) })
	dst := t.TempDir()
	probe := filepath.Join(src, "probe")
	if err := os.WriteFile(probe, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(probe, filepath.Join(dst, "probe")); err == nil {
		t.Skip("临时目录和 /dev/shm 位于同一文件系统")
	} else if !strings.Contains(err.Error(), "cross-device link") {
		t.Fatal(err)
	}
	if err := os.Remove(probe); err != nil {
		t.Fatal(err)
	}
	lock, err := helpers.AcquireInstanceLock(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	files := map[string]string{
		"config.yaml":    "db:\n    engine: sqlite\n    sqliteFile: existing.db\n",
		"encryption.key": "original key",
		"existing.db":    "original database",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(src, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// 按文件名顺序，YAML 先搬走，断链导致跨盘复制失败，密钥和数据库尚未移动。
	brokenLink := filepath.Join(src, "d-broken-link")
	if err := os.Symlink(filepath.Join(src, "missing"), brokenLink); err != nil {
		t.Fatal(err)
	}
	if err := migrateFnOSConfig(src, dst); err == nil {
		t.Fatal("migration unexpectedly succeeded with a broken source file")
	}
	sourceLock, err := helpers.AcquireInstanceLock(src)
	if err != nil {
		t.Fatalf("failed migration retained the source lock: %v", err)
	}
	if err := sourceLock.Close(); err != nil {
		t.Fatal(err)
	}
	if content, err := os.ReadFile(filepath.Join(dst, "config.yaml")); err != nil || string(content) != files["config.yaml"] {
		t.Fatalf("YAML was not moved before failure: %q, %v", content, err)
	}
	if _, err := os.Stat(filepath.Join(src, "config.yaml")); !os.IsNotExist(err) {
		t.Fatalf("source YAML still exists: %v", err)
	}
	if err := os.Remove(brokenLink); err != nil {
		t.Fatal(err)
	}
	if err := migrateFnOSConfig(src, dst); err == nil || !strings.Contains(err.Error(), "迁移尚未完成") {
		t.Fatalf("retry did not reject the incomplete shared instance: %v", err)
	}
	for _, name := range []string{"encryption.key", "existing.db"} {
		if content, err := os.ReadFile(filepath.Join(src, name)); err != nil || string(content) != files[name] {
			t.Errorf("old %s changed after failure: %q, %v", name, content, err)
		}
		if _, err := os.Stat(filepath.Join(dst, name)); !os.IsNotExist(err) {
			t.Errorf("retry changed shared %s: %v", name, err)
		}
	}
}
