package helpers

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const instanceLockFileName = ".qmediasync.lock"

// AcquireInstanceLock 独占配置目录，关闭返回的文件或进程退出时由系统释放锁。
func AcquireInstanceLock(configDir string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(configDir, instanceLockFileName), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开实例锁失败：%w", err)
	}
	if err := lockInstanceFile(file); err != nil {
		return nil, errors.Join(fmt.Errorf("配置目录正被 QMediaSync 或恢复命令使用，请先停止服务：%w", err), file.Close())
	}
	// 锁文件必须保留，删除后其他进程可能锁住不同的 inode。
	return file, nil
}

// MoveConfigDir 锁定旧配置目录后迁移内容；调用方必须已锁定目标目录。
func MoveConfigDir(src, dst string) (err error) {
	source, err := os.Stat(src)
	if err != nil {
		return err
	}
	target, err := os.Stat(dst)
	if err != nil {
		return err
	}
	if os.SameFile(source, target) {
		return nil
	}
	lock, err := AcquireInstanceLock(src)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, lock.Close()) }()
	// 源、目标的锁文件都保留原位，避免另一个进程锁住不同的 inode。
	return MoveDir(src, dst, instanceLockFileName)
}
