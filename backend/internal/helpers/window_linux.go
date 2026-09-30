//go:build !windows

package helpers

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

var ExitChan chan struct{} = make(chan struct{})
var IsFirstRun bool = false // 默认为 false

func StartApp(stopFunc func()) {
}

// StopApp 向自身发送 SIGTERM，与外部停止服务走同一套优雅退出流程。
func StopApp() {
	if err := unix.Kill(os.Getpid(), unix.SIGTERM); err != nil {
		AppLogger.Errorf("发送停止信号失败：%v", err)
	}
}

func StartNewProcess(exePath, updateDir string) bool {
	return true
}

func IsProcessAlive(pid int) (bool, error) {
	return true, nil
}

func OpenBrowser(url string) error {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}

	return cmd.Start()
}

func lockInstanceFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}

// ShowAdminRecoveryResult 直接向终端交付恢复结果，密码不经过日志器。
func ShowAdminRecoveryResult(message string) error {
	_, err := fmt.Fprintln(os.Stdout, message)
	return err
}
