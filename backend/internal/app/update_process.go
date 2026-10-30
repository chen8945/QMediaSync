package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"qmediasync/internal/controllers"
	"qmediasync/internal/helpers"
)

func runUpdateProcess() error {
	if len(os.Args) < 3 {
		return errors.New("更新参数不足")
	}
	updateDir := os.Args[2]
	parentPID := os.Getppid()
	fmt.Printf("等待父进程退出（PID：%d）…\n", parentPID)
	if err := helpers.WaitForProcessExit(parentPID, 70*time.Second); err != nil {
		return fmt.Errorf("等待父进程退出失败：%w", err)
	}

	appPath := filepath.Join(helpers.RootDir, "QMediaSync.exe")
	if err := controllers.InstallReleaseFiles(updateDir, helpers.RootDir, "QMediaSync.exe"); err != nil {
		// 文件替换已回滚，恢复旧版本运行；仍将安装失败返回给更新进程。
		return errors.Join(err, exec.Command(appPath).Start())
	}
	if err := os.RemoveAll(updateDir); err != nil {
		log.Printf("清理更新目录失败：%v", err)
	}
	if err := exec.Command(appPath).Start(); err != nil {
		return fmt.Errorf("启动新版本失败：%w", err)
	}
	fmt.Println("更新完成，新版本已启动")
	return nil
}

func runRestartProcess(args []string) error {
	if len(args) < 2 || args[1] != "--" {
		return errors.New("重启参数不足")
	}
	pid, err := strconv.Atoi(args[0])
	if err != nil || pid <= 0 {
		return errors.New("重启父进程参数无效")
	}
	return helpers.RunAppRestart(pid, args[2:])
}
