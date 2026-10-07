package helpers

import (
	"context"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"testing"
	"time"
)

func TestStopAppForRestartRequestsGracefulExit(t *testing.T) {
	if os.Getenv("QMS_TEST_DELAYED_STOP") == "1" {
		quit := make(chan os.Signal, 1)
		signal.Notify(quit, syscall.SIGTERM)
		defer signal.Stop(quit)
		start := time.Now()
		StopAppForRestart()
		select {
		case <-quit:
			if time.Since(start) < 3*time.Second {
				t.Fatal("stop did not allow HTTP response delivery")
			}
		case <-time.After(6 * time.Second):
			t.Fatal("graceful stop signal was not delivered")
		}
		return
	}
	// 只向隔离测试子进程发信号，不停止开发或生产实例。
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStopAppForRestartRequestsGracefulExit$")
	cmd.Env = append(os.Environ(), "QMS_TEST_DELAYED_STOP=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("delayed stop subprocess: %v\n%s", err, output)
	}
}
