package path

import (
	"fmt"
	"net/http"
	"strings"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/service/openlist"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/util/urls"
)

// OpenlistPathRes 路径转换结果
type OpenlistPathRes struct {

	// Success 转换是否成功
	Success bool

	// Path 转换后的路径
	Path string

	// Range 遍历所有 OpenList 根路径生成的子路径
	Range func() ([]string, error)
}

// Emby2Openlist 将 Emby 资源路径转换为 OpenList 资源路径
func Emby2Openlist(embyPath string) OpenlistPathRes {
	pathRoutes := strings.Builder{}
	pathRoutes.WriteString("[")
	pathRoutes.WriteString("\n【原始路径】 => " + embyPath)

	embyPath = urls.Unescape(embyPath)
	pathRoutes.WriteString("\n\n【URL 解码】 => " + embyPath)

	embyPath = urls.TransferSlash(embyPath)
	pathRoutes.WriteString("\n\n【Windows 反斜杠转换】 => " + embyPath)

	embyMount := config.C.Emby.MountPath
	openlistFilePath := strings.TrimPrefix(embyPath, embyMount)
	pathRoutes.WriteString("\n\n【移除 mount-path】 => " + openlistFilePath)

	if mapPath, ok := config.C.Path.MapEmby2Openlist(openlistFilePath); ok {
		openlistFilePath = mapPath
		pathRoutes.WriteString("\n\n【命中 emby2openlist 映射】 => " + openlistFilePath)
	}
	pathRoutes.WriteString("\n]")
	logs.Tip("Emby 路径转换过程: %s", pathRoutes.String())

	rangeFunc := func() ([]string, error) {
		filePath, err := SplitFromSecondSlash(openlistFilePath)
		if err != nil {
			return nil, fmt.Errorf("解析 OpenList 文件路径失败: %s, 错误: %v", openlistFilePath, err)
		}

		res := openlist.FetchFsList("/", nil)
		if res.Code != http.StatusOK {
			return nil, fmt.Errorf("请求 OpenList fs list 接口失败: %s", res.Msg)
		}

		paths := make([]string, 0, len(res.Data.Content))
		for _, c := range res.Data.Content {
			if !c.IsDir {
				continue
			}
			newPath := fmt.Sprintf("/%s%s", c.Name, filePath)
			paths = append(paths, newPath)
		}
		return paths, nil
	}

	return OpenlistPathRes{
		Success: true,
		Path:    openlistFilePath,
		Range:   rangeFunc,
	}
}

// SplitFromSecondSlash 找到给定字符串 str 中第二个 '/' 字符的位置
// 并以该位置为首字符切割剩余的子串返回
func SplitFromSecondSlash(str string) (string, error) {
	str = urls.TransferSlash(str)
	firstIdx := strings.Index(str, "/")
	if firstIdx == -1 {
		return "", fmt.Errorf("字符串不包含 /: %s", str)
	}

	secondIdx := strings.Index(str[firstIdx+1:], "/")
	if secondIdx == -1 {
		return "", fmt.Errorf("字符串只有单个 /: %s", str)
	}

	return str[secondIdx+firstIdx+1:], nil
}
