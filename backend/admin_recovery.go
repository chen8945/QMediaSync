package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type adminRecoveryOptions struct {
	resetPassword bool
	deleteAdmin   bool
	confirmed     bool
	configDir     string
	fnos          bool
}

func registerAdminRecoveryFlags(flags *flag.FlagSet, options *adminRecoveryOptions) {
	flags.BoolVar(&options.resetPassword, "reset-admin-password", false, "停服后重置管理员密码，清除两步验证和旧浏览器会话")
	flags.BoolVar(&options.deleteAdmin, "delete-admin", false, "停服后删除管理员、全部会话和 API Key，需要 --yes")
	flags.BoolVar(&options.confirmed, "yes", false, "确认删除管理员")
	flags.StringVar(&options.configDir, "config-dir", "", "恢复使用的已有配置目录，默认沿用当前部署路径")
}

func parseAdminRecoveryOptions(args []string) (*adminRecoveryOptions, error) {
	requested := false
	for _, arg := range args {
		name, _, _ := strings.Cut(arg, "=")
		switch name {
		case "-reset-admin-password", "--reset-admin-password", "-delete-admin", "--delete-admin",
			"-config-dir", "--config-dir", "-yes", "--yes":
			requested = true
		}
	}
	if !requested {
		return nil, nil
	}
	options := &adminRecoveryOptions{}
	flags := flag.NewFlagSet("QMediaSync", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	registerAdminRecoveryFlags(flags, options)
	flags.BoolVar(&options.fnos, "fnos", false, "飞牛环境")
	flags.String("guid", "", "沿用部署参数；恢复进程须由部署者以原身份启动")
	if err := flags.Parse(args); err != nil {
		return options, err
	}
	if flags.NArg() != 0 || options.resetPassword == options.deleteAdmin {
		return options, fmt.Errorf("必须且只能指定 --reset-admin-password 或 --delete-admin，不能附加位置参数")
	}
	if options.deleteAdmin && !options.confirmed {
		return options, fmt.Errorf("删除管理员会撤销全部浏览器会话和 QMS API Key，业务数据保留；确认后请附加 --yes")
	}
	return options, nil
}

func resolveConfigDir(explicit string) (string, error) {
	dir := explicit
	if dir == "" {
		dir = filepath.Join(helpers.RootDir, "config")
		if runtime.GOOS != "windows" && os.Getenv("TRIM_PKGETC") != "" {
			dir = os.Getenv("TRIM_PKGETC")
			if share := os.Getenv("TRIM_DATA_SHARE_PATHS"); share != "" {
				dir = filepath.Join(share, "config")
			}
		}
	}
	return filepath.Abs(dir)
}

func performAdminRecovery(ctx context.Context, configDir string, deleteAdmin bool) (result *models.AdminRecoveryResult, err error) {
	info, err := os.Stat(configDir)
	if err != nil {
		return nil, fmt.Errorf("读取已有配置目录失败：%w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("配置路径不是目录")
	}
	lock, err := helpers.AcquireInstanceLock(configDir)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	helpers.ConfigDir = configDir
	if err := checkLegacyDatabaseState(); err != nil {
		return nil, err
	}
	if err := helpers.LoadExistingConfig(); err != nil {
		return nil, err
	}
	logConfig := helpers.LogConfigSnapshot()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(configDir, logConfig.App)), 0o755); err != nil {
		return nil, fmt.Errorf("创建恢复操作日志目录失败：%w", err)
	}
	logger := helpers.NewLogger(logConfig.App, false, true)
	defer logger.Close()
	defer func() {
		if err != nil {
			logger.Errorf("管理员恢复失败：%v", err)
		}
	}()
	conn, err := db.OpenExisting(ctx, configDir, helpers.GlobalConfig.Db)
	if err != nil {
		return nil, err
	}
	sqlDB, err := conn.DB()
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, sqlDB.Close()) }()
	result, err = models.RecoverAdmin(ctx, conn, deleteAdmin)
	if err != nil {
		return nil, err
	}
	if result.Deleted {
		logger.RequiredWarnf("管理员恢复完成：已删除管理员 ID=%d，全部浏览器会话和 QMS API Key 已清理，业务数据保留", result.UserID)
	} else {
		logger.RequiredWarnf("管理员恢复完成：已重置管理员 ID=%d 的密码，浏览器会话已撤销，两步验证已清空，API Key 保留", result.UserID)
	}
	return result, nil
}

func adminRecoveryMessage(result *models.AdminRecoveryResult) string {
	if result.Deleted {
		return fmt.Sprintf("管理员 %s 已删除。\n全部浏览器会话和 QMS API Key 已清理，业务数据保留。\n已重启服务，请从启动日志获取初始化码并重新创建管理员。", result.Username)
	}
	return fmt.Sprintf("管理员密码已重置。\n用户名：%s\n新密码：%s\n全部浏览器会话已撤销，两步验证已关闭，API Key 已保留。\n请保存新密码，启动服务后登录并重新启用两步验证。", result.Username, result.Password)
}

func runAdminRecoveryCommand(args []string) (bool, int) {
	options, err := parseAdminRecoveryOptions(args)
	if options == nil {
		return false, 0
	}
	message := ""
	code := 0
	if err != nil {
		code = 2
		message = err.Error() + "\n用法：QMediaSync --reset-admin-password [--config-dir 目录]\n或：QMediaSync --delete-admin --yes [--config-dir 目录]"
		if errors.Is(err, flag.ErrHelp) {
			code = 0
		}
	} else {
		getRootDir()
		helpers.IsFnOS = options.fnos
		configDir, pathErr := resolveConfigDir(options.configDir)
		err = pathErr
		if err == nil {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			result, recoveryErr := performAdminRecovery(ctx, configDir, options.deleteAdmin)
			err = recoveryErr
			if result != nil {
				message = adminRecoveryMessage(result)
			}
		}
		if err != nil {
			code = 1
			message += "\n管理员恢复命令错误：" + err.Error()
		}
	}
	if err := helpers.ShowAdminRecoveryResult(strings.TrimSpace(message)); err != nil {
		fmt.Fprintln(os.Stderr, "无法交付管理员恢复结果，请重新执行恢复命令：", err)
		return true, 1
	}
	return true, code
}
