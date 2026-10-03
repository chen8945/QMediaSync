package syncstrm

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
	"qmediasync/internal/realtime"
)

func TestLedgerRecoveryKilledProcess(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("本测试通过 Linux SIGKILL 验证实际进程中断")
	}
	if filename := os.Getenv("QMS_LEDGER_KILL_DB"); filename != "" {
		runLedgerKillChild(t, filename, os.Getenv("QMS_LEDGER_KILL_READY"))
		return
	}
	account, syncPath, filename := setupLedgerRecoveryDB(t)
	setupLedgerRecoveryQueues(t)
	writeLedgerRecoveryFiles(t, syncPath.RemotePath, 600)
	workdir := t.TempDir()
	ready := filepath.Join(workdir, "ready")
	command := exec.Command(os.Args[0], "-test.run=^TestLedgerRecoveryKilledProcess$", "-test.count=1", "-test.timeout=30s")
	command.Env = append(os.Environ(), "QMS_LEDGER_KILL_DB="+filename, "QMS_LEDGER_KILL_READY="+ready, "TMPDIR="+workdir)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	waited := false
	defer func() {
		if !waited {
			_ = command.Process.Kill()
			<-done
		}
	}()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	var syncID uint64
	for syncID == 0 {
		data, err := os.ReadFile(ready)
		if err == nil {
			syncID, err = strconv.ParseUint(string(data), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			break
		}
		if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			waited = true
			t.Fatalf("子进程在目标提交边界前退出：%v\n%s", err, output.String())
		case <-ctx.Done():
			t.Fatal("子进程没有到达第二页 SQL")
		case <-time.After(10 * time.Millisecond):
		}
	}
	// 父进程的独立连接只能看到第 1 页，第二页已执行的子批仍在事务里。
	if rows := ledgerRecoveryRows(t, syncPath.ID); len(rows) != 256 {
		t.Fatalf("强杀前未确认第一页已独立提交：rows=%d", len(rows))
	}
	before := readBackgroundTestSync(t, uint(syncID))
	if before.Status != models.SyncStatusCompleted || before.NewStrm != 600 || *before.LedgerStatus != realtime.SyncLedgerRunning {
		t.Fatalf("强杀前任务状态不符：%+v", before)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	err := <-done
	waited = true
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("没有执行真正的 SIGKILL：%v\n%s", err, output.String())
	}
	if rows := ledgerRecoveryRows(t, syncPath.ID); len(rows) != 256 {
		t.Fatalf("强杀后未提交的第二页泄漏：rows=%d", len(rows))
	}
	if err := InitSyncBackgroundService(t.Context()); err != nil {
		t.Fatal(err)
	}
	interrupted := readBackgroundTestSync(t, uint(syncID))
	assertLedgerRecoveryGeneration(t, before, interrupted)
	if *interrupted.LedgerStatus != realtime.SyncLedgerInterrupted || interrupted.LedgerFinishedAt != nil || interrupted.LedgerError == "" {
		t.Fatalf("重启应保留未知结束时间：%+v", interrupted)
	}
	// 重启后远端又少了一项，恢复必须以新扫描为准。
	if err := os.Remove(filepath.Join(syncPath.RemotePath, "movie-0599.mkv")); err != nil {
		t.Fatal(err)
	}
	retry := newLedgerRecoverySync(t, account, syncPath)
	if err := retry.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, retry.backgroundService)
	rows := ledgerRecoveryRows(t, syncPath.ID)
	if len(rows) != 599 || *readBackgroundTestSync(t, retry.Sync.ID).LedgerStatus != realtime.SyncLedgerCompleted {
		t.Fatalf("强杀后的新扫描未恢复：rows=%d", len(rows))
	}
	for _, row := range rows {
		if row.FileName == "movie-0599.mkv" {
			t.Fatal("重启重放了已过期的扫描结果")
		}
	}
	if _, err := os.Stat(filepath.Join(syncPath.LocalPath, "movie-0599.strm")); !os.IsNotExist(err) {
		t.Fatalf("完整重扫未清理已确认消失的文件：%v", err)
	}
}

func runLedgerKillChild(t *testing.T, filename, ready string) {
	t.Helper()
	setupStrmExclusionTestDB(t)
	useLedgerRecoveryDB(t, filename)
	setupLedgerRecoveryQueues(t)
	var syncPath models.SyncPath
	var account models.Account
	if err := db.Db.First(&syncPath).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Db.First(&account, syncPath.AccountId).Error; err != nil {
		t.Fatal(err)
	}
	s := newLedgerRecoverySync(t, &account, &syncPath)
	inserts := 0
	if err := db.Db.Callback().Create().Before("gorm:create").Register("ledger-kill:second-page", func(tx *gorm.DB) {
		if tx.Statement.Table == "sync_files" {
			inserts++
			if inserts == 10 {
				if err := os.WriteFile(ready+".tmp", []byte(strconv.FormatUint(uint64(s.Sync.ID), 10)), 0o600); err != nil {
					tx.AddError(err)
					return
				}
				if err := os.Rename(ready+".tmp", ready); err != nil {
					tx.AddError(err)
					return
				}
				<-tx.Statement.Context.Done()
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	waitBackgroundTest(t, s.backgroundService)
	t.Fatal("子进程未等待 SIGKILL 就执行结束")
}
