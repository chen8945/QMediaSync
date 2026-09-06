package syncstrm

import (
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"qmediasync/internal/models"
)

// 允许上传的目录，网盘中哪怕没有这些目录，也可以上传这些目录下的文件到网盘
var uploadDirNames = []string{
	"extrafanart",
	"exfanarts",
	"extrafanarts",
	"extras",
	"specials",
	"shorts",
	"scenes",
	"featurettes",
	"behind the scenes",
	"trailers",
	"interviews",
	"subs",
}

type SyncStrmConfig struct {
	StrmBaseUrl           string                        `json:"strm_base_url"`             // 视频文件 URL 基础路径
	MinVideoSize          int64                         `json:"min_video_size"`            // 视频文件最小大小，单位为 MB
	EnableDownloadMeta    int64                         `json:"enable_download_meta"`      // 是否下载元数据文件，0 为不下载，1 为下载
	NetNotFoundFileAction models.SyncTreeItemMetaAction `json:"net_not_found_file_action"` // 网盘文件不存在时的操作，0 为忽略，1 为上传，2 为删除
	VideoExt              []string                      `json:"video_ext"`                 // 视频文件扩展名列表
	MetaExt               []string                      `json:"meta_ext"`                  // 元数据文件扩展名列表
	ExcludeNames          []string                      `json:"exclude_names"`             // 排除的文件名列表
	ExcludeNameRegexes    []string                      `json:"exclude_name_regexes"`      // 排除名称的正则表达式原文
	StrmUrlNeedPath       int                           `json:"strm_url_need_path"`        // STRM 链接路径模式，1 为完整路径，2 为只添加文件名，3 为不添加路径
	DelEmptyLocalDir      bool                          `json:"del_empty_local_dir"`       // 是否删除本地空目录
	CheckMetaMtime        int                           `json:"check_meta_mtime"`          // 是否检查元数据文件修改时间，默认 0；如果为 1，网盘新则下载，本地新则上传（UploadMeta=1 时）

	excludeNameRegexes []*regexp.Regexp
}

func (config *SyncStrmConfig) compileExcludeNameRegexes() error {
	compiled := make([]*regexp.Regexp, 0, len(config.ExcludeNameRegexes))
	for i, pattern := range config.ExcludeNameRegexes {
		if pattern == "" {
			return fmt.Errorf("exclude_name_regex_arr[%d]：正则表达式不能为空", i)
		}
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("exclude_name_regex_arr[%d]：正则表达式无效：%w", i, err)
		}
		compiled = append(compiled, expression)
	}
	config.excludeNameRegexes = compiled
	return nil
}

func (s *SyncStrm) ValidFile(file *SyncFileCache) bool {
	if s.IsExcludeName(file.FileName) || s.IsExcludePath(file.GetPath()) {
		s.Sync.Logger.Warnf("文件 %s 的名称或父目录被排除，跳过", file.GetFullRemotePath())
		return false
	}
	// 如果是文件，则进行预处理，然后插入临时表
	file.IsVideo = s.IsValidVideoExt(file.FileName)
	file.IsMeta = s.IsValidMetaExt(file.FileName)
	if !file.IsVideo && !file.IsMeta {
		s.Sync.Logger.Infof("文件 %s 不是视频或元数据，跳过", file.FileName)
		return false
	}
	maxSize := s.GetMinVideoSize()
	if file.IsVideo && file.FileSize < maxSize {
		s.Sync.Logger.Infof("视频文件 %s 大小 %d 小于最低要求 %d，不需要处理", file.FileName, file.FileSize, maxSize)
		return false
	}
	if file.IsMeta && !s.EnableDownloadMeta() {
		// 如果是元数据文件且设置为不下载，则跳过检查（代表着不上传）
		// s.Sync.Logger.Infof("网盘元数据文件 %s 由于关闭了元数据下载所以不需要处理", file.FileName)
		return false
	}
	return true
}
func (s *SyncStrm) GetMinVideoSize() int64 {
	if s.Config.MinVideoSize > 0 {
		return s.Config.MinVideoSize * 1024 * 1024
	}
	return 0
}

func (s *SyncStrm) EnableDownloadMeta() bool {
	if s.Config.EnableDownloadMeta > 0 {
		return true
	}
	return false
}

func (s *SyncStrm) IsValidVideoExt(filename string) bool {
	ext := filepath.Ext(filename)
	ext = strings.ToLower(ext)
	if slices.Contains(s.Config.VideoExt, ext) {
		return true
	}
	return false
}

func (s *SyncStrm) IsValidMetaExt(filename string) bool {
	ext := filepath.Ext(filename)
	ext = strings.ToLower(ext)
	if slices.Contains(s.Config.MetaExt, ext) {
		return true
	}
	return false
}

func (s *SyncStrm) IsExcludeName(filename string) bool {
	if slices.Contains(s.Config.ExcludeNames, strings.ToLower(filename)) {
		return true
	}
	for _, expression := range s.Config.excludeNameRegexes {
		if expression.MatchString(filename) {
			return true
		}
	}
	return false
}

func (s *SyncStrm) IsExcludePath(path string) bool {
	// 分隔路径
	pathParts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range pathParts {
		// 根路径的空段和路径导航符不是实际目录名称。
		if part == "" || part == "." || part == ".." {
			continue
		}
		if s.IsExcludeName(part) {
			return true
		}
	}
	return false
}
