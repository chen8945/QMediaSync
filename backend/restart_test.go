package main

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"qmediasync/internal/helpers"

	"github.com/jackc/pgx/v5/pgproto3"
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
			got, err := helpers.PrepareInitialConfigRestart()
			if err != nil || got != tc.want {
				t.Fatalf("restart=%v err=%v", got, err)
			}
			signal, readErr := os.ReadFile(filepath.Join(helpers.ConfigDir, ".restart-request"))
			if tc.want {
				if readErr != nil || string(signal) != strconv.Itoa(os.Getpid()) {
					t.Fatalf("signal=%q err=%v", signal, readErr)
				}
				if got, err := helpers.PrepareInitialConfigRestart(); got || err == nil {
					t.Fatal("重启准备失败不应报告成功")
				}
			} else if !os.IsNotExist(readErr) {
				t.Fatalf("不支持的入口不应创建信号：%v", readErr)
			}
		})
	}
}

func TestInitialConfigSaveRetryAndConcurrency(t *testing.T) {
	oldDir := helpers.ConfigDir
	t.Cleanup(func() { helpers.ConfigDir = oldDir })
	helpers.ConfigDir = t.TempDir()
	var prepared, exited atomic.Int32
	router := newConfigRouter(func() (bool, error) {
		if prepared.Add(1) == 1 {
			return false, errors.New("request unavailable")
		}
		return true, nil
	}, func() { exited.Add(1) })
	post := func(body string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/config/save", strings.NewReader(body)))
		return response
	}
	if response := post(`{"engine":"unknown"}`); response.Code != 400 || prepared.Load() != 0 {
		t.Fatalf("invalid configuration: %s", response.Body)
	}
	response := post(`{"engine":"sqlite"}`)
	if response.Code != 200 || !strings.Contains(response.Body.String(), `"restartPending":true`) || exited.Load() != 0 {
		t.Fatalf("preparation failure = %d %s", response.Code, response.Body)
	}
	configPath := filepath.Join(helpers.ConfigDir, "config.yaml")
	original, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			// 已保存后不应再次验证、改写配置或执行 PostgreSQL 清库。
			response := post(`{"engine":"postgres","dropDatabase":true}`)
			if response.Code != 200 || !strings.Contains(response.Body.String(), "正在启动 QMediaSync") {
				t.Errorf("retry: %d %s", response.Code, response.Body)
			}
		})
	}
	wg.Wait()
	deadline := time.Now().Add(time.Second)
	for exited.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if prepared.Load() != 2 || exited.Load() != 1 {
		t.Fatalf("prepared=%d exited=%d", prepared.Load(), exited.Load())
	}
	if saved, err := os.ReadFile(configPath); err != nil || string(saved) != string(original) {
		t.Fatal("retry changed saved configuration")
	}
}

func TestInitialConfigSaveFailureAndManualExit(t *testing.T) {
	oldDir := helpers.ConfigDir
	t.Cleanup(func() { helpers.ConfigDir = oldDir })
	helpers.ConfigDir = filepath.Join(t.TempDir(), "missing")
	exit := make(chan struct{}, 1)
	var prepared int
	router := newConfigRouter(func() (bool, error) { prepared++; return false, nil }, func() { exit <- struct{}{} })
	post := func() *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/config/save", strings.NewReader(`{"engine":"sqlite"}`)))
		return response
	}
	if response := post(); response.Code != 500 || prepared != 0 {
		t.Fatalf("save failure: %d %s", response.Code, response.Body)
	}
	if err := os.MkdirAll(helpers.ConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if response := post(); response.Code != 200 || !strings.Contains(response.Body.String(), "按当前部署方式重启") {
		t.Fatalf("manual exit: %d %s", response.Code, response.Body)
	}
	select {
	case <-exit:
	case <-time.After(time.Second):
		t.Fatal("manual entry did not exit")
	}
}

func TestInitialConfigPostgresDropRetry(t *testing.T) {
	oldDir, oldConfig := helpers.ConfigDir, helpers.GlobalConfig
	t.Cleanup(func() { helpers.ConfigDir, helpers.GlobalConfig = oldDir, oldConfig })
	for _, scenario := range []string{"save failure after drop", "drop failure"} {
		t.Run(scenario, func(t *testing.T) {
			helpers.ConfigDir = filepath.Join(t.TempDir(), "config")
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			var drops atomic.Int32
			var denyDrop atomic.Bool
			denyDrop.Store(scenario == "drop failure")
			var connections sync.WaitGroup
			accepted := make(chan struct{})
			go func() {
				defer close(accepted)
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					connections.Go(func() {
						defer conn.Close()
						if err := conn.SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
							t.Error(err)
							return
						}
						server := pgproto3.NewBackend(conn, conn)
						if _, err := server.ReceiveStartupMessage(); err != nil {
							t.Error(err)
							return
						}
						server.Send(&pgproto3.AuthenticationOk{})
						server.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
						if err := server.Flush(); err != nil {
							t.Error(err)
							return
						}
						message, err := server.Receive()
						if err != nil {
							t.Error(err)
							return
						}
						query, ok := message.(*pgproto3.Query)
						if !ok || query.String != `DROP DATABASE IF EXISTS "qms_test"` {
							t.Errorf("unexpected database request: %T", message)
							return
						}
						drops.Add(1)
						if denyDrop.Load() {
							server.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "drop denied"})
						} else {
							server.Send(&pgproto3.CommandComplete{CommandTag: []byte("DROP DATABASE")})
						}
						server.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
						if err := server.Flush(); err != nil {
							t.Error(err)
						}
					})
				}
			}()
			t.Cleanup(func() {
				if err := listener.Close(); err != nil {
					t.Error(err)
				}
				<-accepted
				connections.Wait()
			})
			body, err := json.Marshal(databaseConfigRequest{
				Engine: helpers.DbEnginePostgres, Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port,
				User: "qms_test", Database: "qms_test", DropDatabase: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			var prepared atomic.Int32
			router := newConfigRouter(func() (bool, error) { prepared.Add(1); return true, nil }, func() {})
			post := func(value []byte) *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/api/config/save", strings.NewReader(string(value))))
				return response
			}
			first := post(body)
			if scenario == "drop failure" {
				if first.Code != 200 || !strings.Contains(first.Body.String(), "清空数据库失败") || strings.Contains(first.Body.String(), `"success":true`) {
					t.Fatalf("failed drop claimed a saved configuration: %d %s", first.Code, first.Body)
				}
				denyDrop.Store(false)
			} else {
				if first.Code != 500 || !strings.Contains(first.Body.String(), "数据库已清空") {
					t.Fatalf("save failure after drop: %d %s", first.Code, first.Body)
				}
				// 已清空数据库的保存重试固定使用原设置，不接受另一套库配置。
				body = []byte(`{"engine":"sqlite"}`)
			}
			if drops.Load() != 1 || prepared.Load() != 0 || helpers.HasConfigFile() {
				t.Fatal("failed initialization must retain the configuration service without writing YAML")
			}
			if err := os.Mkdir(helpers.ConfigDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if retry := post(body); retry.Code != 200 || !strings.Contains(retry.Body.String(), `"success":true`) {
				t.Fatalf("save retry: %d %s", retry.Code, retry.Body)
			}
			wantDrops := int32(1)
			if scenario == "drop failure" {
				wantDrops = 2
			}
			if drops.Load() != wantDrops || prepared.Load() != 1 {
				t.Fatalf("drops=%d prepared=%d", drops.Load(), prepared.Load())
			}
			if err := helpers.LoadExistingConfig(); err != nil {
				t.Fatal(err)
			}
			if helpers.GlobalConfig.Db.Engine != helpers.DbEnginePostgres || helpers.GlobalConfig.Db.PostgresConfig.Database != "qms_test" {
				t.Fatal("retry changed the database settings after a completed drop")
			}
		})
	}
}
