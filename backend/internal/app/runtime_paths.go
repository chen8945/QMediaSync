package app

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"qmediasync/internal/helpers"
)

var instanceLock *os.File

func checkRelease() {
	if helpers.IsRunningInDocker() {
		helpers.IsRelease = true
	}
	arg1 := strings.ToLower(os.Args[0])
	name := strings.ToLower(filepath.Base(arg1))
	helpers.IsRelease = strings.Index(name, "qmediasync") == 0 && !strings.Contains(arg1, "go-build")
}

func getRootDir() string {
	var exPath string = "/app" // 默认使用 Docker 路径
	checkRelease()
	if os.Getenv("TRIM_APPDEST") != "" {
		helpers.RootDir = os.Getenv("TRIM_APPDEST")
		return helpers.RootDir
	}
	if helpers.IsRelease {
		ex, err := os.Executable()
		if err != nil {
			panic(err)
		}
		exPath = filepath.Dir(ex)
	} else {
		exPath, _ = os.Getwd()
	}
	helpers.RootDir = exPath // 获取当前工作目录
	return exPath
}

// 获取用户数据目录
func getDataAndConfigDir() error {
	resolvedDir, err := resolveConfigDir("")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(resolvedDir, 0o755); err != nil {
		return err
	}
	lock, err := helpers.AcquireInstanceLock(resolvedDir)
	if err != nil {
		return err
	}
	instanceLock = lock

	var appData string
	var configDir string
	needMk := false
	if runtime.GOOS == "windows" {
		// 使用 AppData 目录，用户有完全控制权限
		appData := os.Getenv("LOCALAPPDATA")
		if appData == "" {
			appData = os.Getenv("APPDATA")
		}
		oldConfigDir := filepath.Join(appData, build.AppName, "config") // 配置目录
		configDir = filepath.Join(helpers.RootDir, "config")            // 配置目录
		err := os.MkdirAll(configDir, 0755)
		if err != nil {
			fmt.Printf("创建配置目录失败：%v\n", err)
			panic("创建配置目录失败")
		}
		helpers.ConfigDir = configDir
		if helpers.PathExists(oldConfigDir) {
			// 迁移旧配置
			err := helpers.MoveConfigDir(oldConfigDir, configDir)
			if err != nil {
				return fmt.Errorf("迁移旧配置目录失败：%w", err)
			}
		}
	} else {
		if os.Getenv("TRIM_PKGETC") == "" {
			appData = helpers.RootDir
			configDir = filepath.Join(appData, "config") // 配置目录
			needMk = true
			helpers.ConfigDir = configDir
		} else {
			oldConfigDir := os.Getenv("TRIM_PKGETC")
			configDir = os.Getenv("TRIM_DATA_SHARE_PATHS")
			if configDir == "" {
				configDir = oldConfigDir
				needMk = false
			} else {
				configDir = filepath.Join(configDir, "config")
				needMk = true
				if err := migrateFnOSConfig(oldConfigDir, configDir); err != nil {
					return err
				}
			}
			helpers.ConfigDir = configDir
		}
	}
	if needMk {
		err := os.MkdirAll(configDir, 0755)
		if err != nil {
			log.Printf("创建配置目录失败：%v\n", err)
			panic("创建配置目录失败")
		}
	}
	return nil
}

const fnOSConfigMigrationPendingFile = ".fnos-config-migration-pending"

// migrateFnOSConfig 仅向空共享目录迁移；中断后停止启动，避免把部分文件当成完整实例。
func migrateFnOSConfig(src, dst string) (err error) {
	pending := filepath.Join(dst, fnOSConfigMigrationPendingFile)
	if _, err := os.Lstat(pending); err == nil {
		return fmt.Errorf("fnOS 配置迁移尚未完成，已停止启动以保护原数据；请先备份旧目录 %s 和共享目录 %s，确认配置、密钥和数据库完整后再移除标记 %s", src, dst, pending)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查 fnOS 配置迁移状态失败：%w", err)
	}
	if filepath.Clean(src) == filepath.Clean(dst) {
		return nil
	}
	source, err := os.ReadDir(src)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取 fnOS 旧配置目录失败：%w", err)
	}
	hasData := func(entries []os.DirEntry) bool {
		for _, entry := range entries {
			if entry.Name() != ".qmediasync.lock" {
				return true
			}
		}
		return false
	}
	if !hasData(source) {
		return nil
	}
	for _, name := range []string{"config.yaml", "config.yml"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err == nil {
			log.Printf("继续使用 fnOS 共享目录 %s 中的配置；旧目录 %s 中的数据已保留，确认备份后可手工整理。", dst, src)
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("检查 fnOS 共享配置失败：%w", err)
		}
	}
	target, err := os.ReadDir(dst)
	if err != nil {
		return fmt.Errorf("读取 fnOS 共享目录失败：%w", err)
	}
	if hasData(target) {
		return fmt.Errorf("fnOS 旧目录 %s 和共享目录 %s 均有数据，已停止迁移以避免覆盖；请先备份并确认要使用的完整配置目录", src, dst)
	}
	lock, err := helpers.AcquireInstanceLock(src)
	if err != nil {
		return fmt.Errorf("锁定 fnOS 旧配置目录失败：%w", err)
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	// 取得源锁后才记录迁移开始，逐文件移动中断时不能用 YAML 判断完成。
	if err := os.WriteFile(pending, []byte(src+"\n"), 0o600); err != nil {
		return fmt.Errorf("记录 fnOS 配置迁移状态失败：%w", err)
	}
	if err := helpers.MoveDir(src, dst, ".qmediasync.lock"); err != nil {
		return fmt.Errorf("迁移 fnOS 旧配置失败，已保留现有数据，请检查两个目录：%w", err)
	}
	if err := os.Remove(pending); err != nil {
		return fmt.Errorf("清除 fnOS 配置迁移标记失败，已停止启动：%w", err)
	}
	return nil
}

// checkLegacyDatabaseState 在任何数据库写入前拒绝未处理的旧内嵌数据库状态。
func checkLegacyDatabaseState() error {
	backupPath := filepath.Join(helpers.ConfigDir, "backups", "migrate.zip")
	if _, err := os.Lstat(backupPath); err == nil {
		return fmt.Errorf("发现遗留迁移包 %s，此版本已不提供自动迁移，请先妥善处理旧数据；迁移包已保留", backupPath)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧迁移包失败：%w", err)
	}
	if helpers.HasConfigFile() {
		return nil
	}
	postgresDir := filepath.Join(helpers.ConfigDir, "postgres")
	if _, err := os.Lstat(postgresDir); err == nil {
		return fmt.Errorf("缺少配置文件，但发现旧 PostgreSQL 数据目录 %s，此版本已移除内嵌数据库和自动迁移，请先妥善处理旧数据；目录已保留", postgresDir)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("检查旧 PostgreSQL 数据目录失败：%w", err)
	}
	return nil
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
