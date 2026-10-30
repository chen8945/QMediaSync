package openlist

import "encoding/base64"

// PathEncode 编码 OpenList 资源原始路径, 避免路径在传输过程中出错
func PathEncode(rawPath string) string {
	return base64.StdEncoding.EncodeToString([]byte(rawPath))
}

// PathDecode 解码 OpenList 编码路径并返回原始路径
//
// 如果解码失败, 则返回原路径
func PathDecode(encPath string) string {
	res, err := base64.StdEncoding.DecodeString(encPath)
	if err != nil {
		return encPath
	}
	return string(res)
}
