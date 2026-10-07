package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/helpers"
)

func TestRestartCommandRejectsInvalidArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"1"}, {"1", "invalid"}, {"invalid", "--"}, {"0", "--"}, {"-1", "--"}} {
		if err := runRestartProcess(args); err == nil {
			t.Fatalf("accepted invalid restart arguments: %q", args)
		}
	}
	if runtime.GOOS != "windows" && runRestartProcess([]string{"1", "--"}) == nil {
		t.Fatal("non-Windows platform accepted standalone restart child")
	}
}

func TestDockerRestartEntrypoint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("需要 POSIX shell")
	}
	entrypoint, err := os.ReadFile("../docker/entrypoint.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"restart", "normal exit", "stale signal", "wrong pid"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, contents string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write("entrypoint.sh", strings.ReplaceAll(string(entrypoint), "/app", root))
			write("scripts/watch_update.sh", "#!/bin/sh\nexit 0\n")
			write("config/.USER", "0:0\n")
			write("QMediaSync", `#!/bin/sh
test "$DOCKER" = 1 && test "$QMS_DOCKER_RESTART" = 1 || exit 20
echo start >> starts
if [ "$(wc -l < starts)" -eq 1 ]; then
    case "$QMS_TEST_RESTART_SCENARIO" in
        restart) echo "$$" > config/.restart-request ;;
        "wrong pid") echo invalid > config/.restart-request ;;
    esac
fi
`)
			if scenario == "stale signal" {
				write("config/.restart-request", "stale")
			}
			t.Setenv("GUID", "")
			t.Setenv("GPID", "")
			t.Setenv("QMS_TEST_RESTART_SCENARIO", scenario)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			output, err := exec.CommandContext(ctx, "sh", filepath.Join(root, "entrypoint.sh")).CombinedOutput()
			if err != nil {
				t.Fatalf("entrypoint: %v\n%s", err, output)
			}
			starts, err := os.ReadFile(filepath.Join(root, "starts"))
			want := "start\n"
			if scenario == "restart" {
				want += "start\n"
			}
			if err != nil || string(starts) != want {
				t.Fatalf("starts = %q, want %q, error %v\n%s", starts, want, err, output)
			}
			if strings.Contains(string(output), "检测到新版本") {
				t.Fatalf("pure restart entered update flow: %s", output)
			}
		})
	}
}

func TestInitialConfigDockerRestart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Docker 入口运行于 Linux")
	}
	oldDir, oldFnOS := helpers.ConfigDir, helpers.IsFnOS
	t.Cleanup(func() { helpers.ConfigDir, helpers.IsFnOS = oldDir, oldFnOS })
	helpers.IsFnOS = false
	for _, tc := range []struct {
		name, docker, support string
		want                  bool
	}{
		{"supported Docker", "1", "1", true},
		{"old Docker", "1", "", false},
		{"non Docker", "", "1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helpers.ConfigDir = t.TempDir()
			t.Setenv("DOCKER", tc.docker)
			t.Setenv("QMS_DOCKER_RESTART", tc.support)
			got, err := prepareInitialConfigRestart()
			if err != nil || got != tc.want {
				t.Fatalf("restart=%v err=%v", got, err)
			}
			signal, readErr := os.ReadFile(filepath.Join(helpers.ConfigDir, ".restart-request"))
			if tc.want {
				if readErr != nil || string(signal) != strconv.Itoa(os.Getpid()) {
					t.Fatalf("signal=%q err=%v", signal, readErr)
				}
				if got, err := prepareInitialConfigRestart(); got || err == nil {
					t.Fatal("重启准备失败不应报告成功")
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("不支持的入口不应创建信号：%v", readErr)
			}
		})
	}
}
