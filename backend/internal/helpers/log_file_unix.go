//go:build !windows

package helpers

import "os"

func openAppendLogFile(name string) (*os.File, error) {
	return os.OpenFile(name, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
}
