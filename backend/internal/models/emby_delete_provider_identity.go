package models

import (
	"path"
	"strconv"
)

// 百度账本保留路径作为 FileID、fsid 作为 PickCode；只在调用网盘时转换，
// 不重写冻结数据及其去重键。历史数值 FileID 仍可使用，冲突身份不猜测。
func embyFrozenPhysicalID(file EmbyFrozenFile) string {
	if file.SourceType != SourceTypeBaiduPan {
		return file.FileID
	}
	validID := func(value string) bool {
		id, err := strconv.ParseInt(value, 10, 64)
		return err == nil && id > 0 && strconv.FormatInt(id, 10) == value
	}
	if validID(file.FileID) {
		if file.PickCode == "" || file.PickCode == file.FileID {
			return file.FileID
		}
		return ""
	}
	if validID(file.PickCode) && embyRemoteDirectoriesMatch(file.SourceType, file.FileID, path.Join(file.Path, file.FileName)) {
		return file.PickCode
	}
	return ""
}
