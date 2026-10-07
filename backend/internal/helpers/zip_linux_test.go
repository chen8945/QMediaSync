package helpers

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestZipDirReportsFinalWriteFailure(t *testing.T) {
	if os.Getenv("QMS_TEST_ZIP_CLOSE_FAILURE") == "1" {
		src := t.TempDir()
		dst := filepath.Join(t.TempDir(), "backup.zip")
		var limit syscall.Rlimit
		if err := syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit); err != nil {
			t.Fatal(err)
		}
		signal.Ignore(syscall.SIGXFSZ)
		if err := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &syscall.Rlimit{Cur: 0, Max: limit.Max}); err != nil {
			t.Fatal(err)
		}
		// 空目录没有条目写入，首次写文件发生在 ZIP Close 刷新中央目录时。
		err := ZipDir(src, dst)
		if restoreErr := syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit); restoreErr != nil {
			t.Fatal(restoreErr)
		}
		if !errors.Is(err, syscall.EFBIG) {
			t.Fatalf("ZIP Close must propagate its write failure: %v", err)
		}
		if info, statErr := os.Stat(dst); statErr != nil || !info.Mode().IsRegular() || info.Size() != 0 {
			t.Fatalf("ZIP destination must have been created before the failed final write: %v", statErr)
		}
		return
	}
	// 文件大小限制和信号处置只作用于隔离子进程，不改变测试主进程或运行中的应用。
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestZipDirReportsFinalWriteFailure$")
	command.Env = append(os.Environ(), "QMS_TEST_ZIP_CLOSE_FAILURE=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ZIP final write failure subprocess: %v\n%s", err, output)
	}
}
