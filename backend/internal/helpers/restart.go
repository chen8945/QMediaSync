package helpers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

// SupportsAppRestart 判断当前入口能否在应用退出后重新拉起服务。
func SupportsAppRestart() bool {
	if IsFnOS {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Getenv("DOCKER") == "1" {
		// 老入口只处理更新包，不能仅凭 DOCKER 标记承诺纯重启。
		return os.Getenv("QMS_DOCKER_RESTART") == "1"
	}
	return os.Getenv("QMS_SYSTEMD_UPDATE") == "1" && os.Getenv("INVOCATION_ID") != ""
}

// RestartApp 准备本轮重启后延迟退出；准备失败时保留当前进程供用户重试。
func RestartApp() error {
	if err := prepareAppRestart(); err != nil {
		return err
	}
	go StopAppForRestart()
	return nil
}

func prepareAppRestart() error {
	if !SupportsAppRestart() {
		return errors.New("当前运行方式不支持程序内重启")
	}
	if runtime.GOOS == "windows" {
		if err := startRestartProcess(); err != nil {
			return err
		}
	} else if os.Getenv("DOCKER") == "1" {
		if ConfigDir == "" {
			return errors.New("尚未初始化配置目录")
		}
		// 入口只消费与刚退出子进程 PID 匹配的信号，遗留信号不会触发下一轮重启。
		path := filepath.Join(ConfigDir, ".restart-request")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return fmt.Errorf("准备 Docker 重启信号失败：%w", err)
		}
		_, writeErr := file.WriteString(strconv.Itoa(os.Getpid()))
		if err := errors.Join(writeErr, file.Close()); err != nil {
			return fmt.Errorf("写入 Docker 重启信号失败：%w", errors.Join(err, os.Remove(path)))
		}
	}
	return nil
}

// StopAppForRestart 留出结果交付时间，再沿用正常退出流程；关闭超时则强制退出。
func StopAppForRestart() {
	time.Sleep(3 * time.Second)
	time.AfterFunc(60*time.Second, func() { os.Exit(1) })
	StopApp()
}

// WaitForProcessExit 等待旧进程退出并释放文件，供 Windows 更新和纯重启共用。
func WaitForProcessExit(pid int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		alive, err := IsProcessAlive(pid)
		if err != nil {
			return err
		}
		if !alive {
			time.Sleep(2 * time.Second)
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("进程 %d 在 %s 内未退出", pid, timeout)
}
