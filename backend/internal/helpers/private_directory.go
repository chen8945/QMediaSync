package helpers

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsurePrivateDir 只收紧 base 下显式列出的受管目录，不改变配置根目录权限。
func EnsurePrivateDir(base string, names ...string) error {
	for _, name := range names {
		if name == "." || name == ".." || filepath.Base(name) != name {
			return fmt.Errorf("私有目录名称无效")
		}
		base = filepath.Join(base, name)
		if err := os.Mkdir(base, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, err := os.Lstat(base)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("私有目录不是普通目录或包含符号链接")
		}
		if err := os.Chmod(base, 0700); err != nil {
			return err
		}
	}
	return nil
}
