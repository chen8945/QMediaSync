//go:build windows

package helpers

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
