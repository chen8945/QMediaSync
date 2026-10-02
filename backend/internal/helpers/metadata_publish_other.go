//go:build !linux && !windows

package helpers

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
)

func metadataLinkUnsupported(_ string, err error) bool {
	return errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.ENOSYS)
}

func renameMetadataNoReplace(_, _ string) error {
	return fmt.Errorf("%s 平台未实现原生非覆盖重命名", runtime.GOOS)
}
