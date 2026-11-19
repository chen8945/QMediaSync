package logstream

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func fileIdentity(file *os.File, _ os.FileInfo) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x-%x-%x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
