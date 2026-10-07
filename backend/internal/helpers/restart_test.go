package helpers

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

func TestSupportsAppRestart(t *testing.T) {
	oldFnOS := IsFnOS
	t.Cleanup(func() { IsFnOS = oldFnOS })
	for _, tc := range []struct {
		name, docker, dockerRestart, systemd, invocation string
		fnos, wantLinux                                  bool
	}{
		{name: "manual"},
		{name: "old Docker entrypoint", docker: "1"},
		{name: "new Docker entrypoint", docker: "1", dockerRestart: "1", wantLinux: true},
		{name: "entrypoint marker alone", dockerRestart: "1"},
		{name: "systemd", systemd: "1", invocation: "current", wantLinux: true},
		{name: "systemd marker alone", systemd: "1"},
		{name: "systemd process alone", invocation: "current"},
		{name: "Docker does not borrow systemd", docker: "1", systemd: "1", invocation: "current"},
		{name: "FnOS", docker: "1", dockerRestart: "1", fnos: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DOCKER", tc.docker)
			t.Setenv("QMS_DOCKER_RESTART", tc.dockerRestart)
			t.Setenv("QMS_SYSTEMD_UPDATE", tc.systemd)
			t.Setenv("INVOCATION_ID", tc.invocation)
			IsFnOS = tc.fnos
			want := !tc.fnos && (runtime.GOOS == "windows" || runtime.GOOS == "linux" && tc.wantLinux)
			if got := SupportsAppRestart(); got != want {
				t.Fatalf("SupportsAppRestart() = %t, want %t", got, want)
			}
		})
	}
	IsFnOS = true
	if err := RestartApp(); err == nil {
		t.Fatal("unsupported restart must fail before preparing an exit")
	}
}

func TestPrepareDockerAppRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker 入口运行于 Linux")
	}
	oldFnOS, oldConfigDir := IsFnOS, ConfigDir
	t.Cleanup(func() { IsFnOS, ConfigDir = oldFnOS, oldConfigDir })
	IsFnOS = false
	t.Setenv("DOCKER", "1")
	t.Setenv("QMS_DOCKER_RESTART", "1")
	ConfigDir = t.TempDir()
	if err := PrepareAppRestart(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(ConfigDir, ".restart-request")
	data, err := os.ReadFile(path)
	if err != nil || string(data) != strconv.Itoa(os.Getpid()) {
		t.Fatalf("restart signal = %q, %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("restart signal permissions: %v, %v", info, err)
	}
	if err := PrepareAppRestart(); err == nil {
		t.Fatal("existing restart signal must not be overwritten")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(ConfigDir, "untouched")
	if err := os.WriteFile(target, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if err := PrepareAppRestart(); err == nil {
		t.Fatal("symlink restart signal must fail")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "original" {
		t.Fatalf("symlink target changed: %q, %v", data, err)
	}
	ConfigDir = path // 文件不能作为目录，准备必须失败且不得安排退出。
	if err := PrepareAppRestart(); err == nil {
		t.Fatal("unwritable restart signal must fail")
	}
	ConfigDir = ""
	if err := PrepareAppRestart(); err == nil {
		t.Fatal("uninitialized config directory must fail")
	}
}
