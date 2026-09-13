package helpers

import (
	"path"
	"strings"
)

// V115PlaybackDirectory 是 115 根目录下保留的多端播放临时目录。
const V115PlaybackDirectory = "/多端播放"

// IsV115PlaybackPath 判断 115 完整路径是否位于多端播放临时目录中。
// 115 列表和详情可能省略首个斜杠；调用方负责限制来源为 115。
func IsV115PlaybackPath(remotePath string) bool {
	if remotePath == "" {
		return false
	}
	remotePath = path.Clean("/" + strings.ReplaceAll(remotePath, "\\", "/"))
	return remotePath == V115PlaybackDirectory || strings.HasPrefix(remotePath, V115PlaybackDirectory+"/")
}
