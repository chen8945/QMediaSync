//go:build windows

package helpers

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestStopAppWaitsForShutdownWithoutTray(t *testing.T) {
	oldExit := ExitChan
	ExitChan = make(chan struct{})
	stopAppOnce = sync.Once{}
	t.Cleanup(func() {
		ExitChan = oldExit
		stopAppOnce = sync.Once{}
	})
	stopped := make(chan struct{})
	go func() {
		StopApp()
		close(stopped)
	}()
	select {
	case <-ExitChan:
	case <-time.After(time.Second):
		t.Fatal("stop did not request application shutdown")
	}
	select {
	case <-stopped:
		t.Fatal("stop returned before application cleanup completed")
	default:
	}
	close(ExitChan)
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("stop did not finish after application cleanup")
	}
	StopApp() // 重复调用不能再次向已关闭通道发送信号。
}

func TestRestartChildArgumentsAndDirectory(t *testing.T) {
	if output := os.Getenv("QMS_TEST_RESTART_OUTPUT"); output != "" {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		data, err := json.Marshal([]any{cwd, os.Args[1:]})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(output, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := startDetachedAppProcess(filepath.Join(t.TempDir(), "missing.exe"), nil); err == nil {
		t.Fatal("starting a missing executable must fail")
	}
	dir := t.TempDir()
	t.Chdir(dir)
	output := filepath.Join(dir, "child.json")
	t.Setenv("QMS_TEST_RESTART_OUTPUT", output)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestRestartChildArgumentsAndDirectory$", "--", "--guid", "name with spaces"}
	if err := startDetachedAppProcess(exe, args); err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal([]any{dir, args})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(output)
		if err == nil && bytes.Equal(data, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("child did not preserve arguments and working directory")
}

func init() {
	if os.Getenv("QMS_TEST_INITIAL_RESTART_DIR") == "" || len(os.Args) < 4 || os.Args[1] != "--restart-parent" {
		return
	}
	pid, err := strconv.Atoi(os.Args[2])
	if err == nil {
		err = RunAppRestart(pid, os.Args[4:])
	}
	if err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestInitialConfigWindowsRestart(t *testing.T) {
	if dir := os.Getenv("QMS_TEST_INITIAL_RESTART_DIR"); dir != "" {
		lock, err := AcquireInstanceLock(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		prepared := filepath.Join(dir, "prepared")
		if _, err := os.Stat(prepared); err == nil {
			cwd, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal([]any{cwd, os.Args[1:]})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "restarted.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			return
		}
		IsFnOS = false
		ok, err := PrepareInitialConfigRestart()
		if !ok || err != nil {
			t.Fatalf("initial restart=%v %v", ok, err)
		}
		if err := os.WriteFile(prepared, []byte("saved"), 0o600); err != nil {
			t.Fatal(err)
		}
		// 模拟首次配置服务交付响应后退出，实例锁由进程退出释放。
		time.Sleep(500 * time.Millisecond)
		os.Exit(0)
	}
	dir := filepath.Join(t.TempDir(), "QMS 中文 path")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("QMS_TEST_INITIAL_RESTART_DIR", dir)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-test.run=^TestInitialConfigWindowsRestart$", "--", "--guid", "original value"}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = dir
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("configuration parent: %v\n%s", err, output)
	}
	want, err := json.Marshal([]any{dir, args})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(dir, "restarted.json")); err == nil && bytes.Equal(data, want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("restart did not preserve directory/arguments or release the old instance lock")
}
