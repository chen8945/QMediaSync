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
		for _, mode := range []string{"recovery_failure", "success"} {
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
				if mode == "success" {
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
}
