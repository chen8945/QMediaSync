package syncstrm

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

type StrmData struct {
	UserId   string `json:"userid"`    // 用户 ID
	PickCode string `json:"pick_code"` // 文件 ID
	Sign     string `json:"sign"`      // 文件签名
	Path     string `json:"path"`      // 115 的路径
	BaseUrl  string `json:"base_url"`  // 115 的 base URL
	UrlPath  string `json:"url_path"`  // 115 的 URL path
	RawQuery string `json:"raw_query"` // STRM URL 的原始 query
}

// 生成 STRM 文件
// st 只能是来源路径，所以需要生成 STRM 文件的路径
func (s *SyncStrm) ProcessStrmFile(sf *SyncFileCache) error {
	changed, err := func() (bool, error) {
		unlock := lockStrmTarget(sf.GetLocalFilePath(s.TargetPath, s.SourcePath))
		defer unlock()
		if s.CompareStrm(sf) == 1 {
			return false, nil
		}
		err := s.writeStrmFile(sf)
		return err == nil, err
	}()
	if err != nil || !changed {
		return err
	}
	s.recordEmbyRefreshTarget(sf.GetSyncFile(s, s.Account.BaseUrl))
	atomic.AddInt64(&s.NewStrm, 1)
	s.PublishProgress(false)
	return nil
}

func (s *SyncStrm) writeStrmFile(sf *SyncFileCache) error {
	// localFilePath := sf.GetLocalFilePath()
	strmFullPath := sf.GetLocalFilePath(s.TargetPath, s.SourcePath)
	strmContent := s.SyncDriver.MakeStrmContent(sf)
	if strmContent == "" {
		s.Sync.Logger.Errorf("生成 STRM 文件内容失败，可能是 STRM 直连地址格式不正确：%s", filepath.Join(sf.Path, sf.FileName))
		return fmt.Errorf("生成 STRM 文件内容失败")
	}

	// 写入文件并设置所有者
	err := helpers.WriteFileWithPerm(strmFullPath, []byte(strmContent), 0777)
	if err != nil {
		s.Sync.Logger.Errorf("写入 STRM 文件并设置所有者失败：%v", err)
		return err
	}
	// 修改文件时间
	if sf.MTime > 0 {
		err := os.Chtimes(strmFullPath, time.Unix(sf.MTime, 0), time.Unix(sf.MTime, 0))
		if err != nil {
			s.Sync.Logger.Errorf("修改 STRM 文件时间失败：%v", err)
			return err
		}
	}
	s.Sync.Logger.Infof("[生成 STRM] %s => %s", strmFullPath, strmContent)
	return nil
}

// 1-无需操作，0-更新
func (s *SyncStrm) CompareStrm(st *SyncFileCache) int {
	localFilePath := st.GetLocalFilePath(s.TargetPath, s.SourcePath)
	if !helpers.PathExists(localFilePath) {
		// s.Sync.Logger.Infof("文件 %s 不存在，需要生成 STRM 文件", st.LocalFilePath)
		return 0
	}
	if st.SourceType == models.SourceTypeLocal {
		// s.Sync.Logger.Infof("文件 %s 来源本地，不需要生成 STRM 文件", filepath.Join(st.Path, st.FileName))
		return 1
	}
	// 读取 STRM 文件内容
	strmData := s.LoadDataFromStrm(localFilePath)
	if strmData == nil {
		return 0
	}
	if st.SourceType == models.SourceTypeOpenList {
		account, err := models.GetAccountById(s.Account.ID)
		if err != nil {
			s.Sync.Logger.Errorf("获取 OpenList 账号信息失败：%v", err)
			return 0
		}
		baseUrl := s.Config.StrmBaseUrl
		if baseUrl == "" {
			baseUrl = account.BaseUrl
		}
		// 如果 baseURL 以 / 结尾，则删掉结尾的 /。
		if before, ok := strings.CutSuffix(baseUrl, "/"); ok {
			baseUrl = before
		}
		// 比较主机名称是否相同
		if strmData.BaseUrl != baseUrl {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容主机名与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), baseUrl, strmData.BaseUrl)
			return 0
		}
		if strmData.Sign != st.OpenlistSign {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容签名参数与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), st.OpenlistSign, strmData.Sign)
			return 0
		}
	}
	if st.SourceType == models.SourceType115 || st.SourceType == models.SourceTypeBaiduPan {
		// 比较路径是否相同
		expectedPath := expectedStrmPathForSyncFile(s.Config.StrmUrlNeedPath, st)
		if expectedPath != "" {
			if strmData.Path != expectedPath {
				s.Sync.Logger.Warnf("文件 %s 的 STRM 内容路径与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), expectedPath, strmData.Path)
				return 0
			}
		} else if strmData.Path != "" {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容包含路径 %s，但设置中已关闭添加路径，将重新生成 STRM 以移除路径", filepath.Join(st.Path, st.FileName), strmData.Path)
			return 0
		}
		// 比较主机名称是否相同
		// 如果 StrmBaseUrl 以 / 结尾，则删除末尾的 /。
		baseURL := strings.TrimSuffix(s.Config.StrmBaseUrl, "/")
		if strmData.BaseUrl != baseURL {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容主机名与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), baseURL, strmData.BaseUrl)
			return 0
		}
		// 如果没有 PickCode，则更新以补全。
		if strmData.PickCode == "" {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容缺少 PickCode：%s，将补全", filepath.Join(st.Path, st.FileName), strmData.PickCode)
			return 0
		} else {
			if strmData.PickCode != st.PickCode {
				s.Sync.Logger.Warnf("文件 %s 的 STRM 内容 PickCode 与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), st.PickCode, strmData.PickCode)
				return 0
			}
		}
		if strmData.UserId != s.Account.UserId {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容用户 ID 与本地不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), s.Account.UserId, strmData.UserId)
			return 0
		}
		// 比较 URLPath，如果是 ISO 文件，URLPath 必须以 .iso 结尾。
		ext := filepath.Ext(st.FileName)
		if !strings.HasSuffix(strmData.UrlPath, ext) {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容 URL 路径 %s 没有以 %s 结尾，重新生成", filepath.Join(st.Path, st.FileName), strmData.UrlPath, ext)
			return 0
		}
		expectedRawQuery := expectedStrmQueryForSyncFile(s.Config.StrmUrlNeedPath, st, s.Account.UserId)
		if strmData.RawQuery != expectedRawQuery {
			s.Sync.Logger.Warnf("文件 %s 的 STRM 内容 query 顺序或编码与当前规范不一致，本地：%s，远程：%s", filepath.Join(st.Path, st.FileName), expectedRawQuery, strmData.RawQuery)
			return 0
		}
	}
	return 1
}

// 解析 STRM 文件内 URL 的参数并返回
func (s *SyncStrm) LoadDataFromStrm(strmPath string) *StrmData {
	if !helpers.PathExists(strmPath) {
		// s.Sync.Logger.Errorf("STRM 文件不存在：%s", strmPath)
		return nil
	}
	data, err := os.ReadFile(strmPath)
	if err != nil {
		s.Sync.Logger.Errorf("读取 STRM 文件失败：%v", err)
		return nil
	}
	var strmData StrmData
	strmUrl, urlErr := url.Parse(string(data))
	if urlErr != nil {
		s.Sync.Logger.Errorf("解析 STRM 文件失败：%v", urlErr)
		return nil
	}
	strmData.UrlPath = strmUrl.Path
	strmData.RawQuery = strmUrl.RawQuery
	queryParams := strmUrl.Query()
	if pickCode := queryParams.Get("pickcode"); pickCode != "" {
		strmData.PickCode = pickCode
	}
	if userId := queryParams.Get("userid"); userId != "" {
		strmData.UserId = userId
	}
	strmData.Sign = ""
	if sign := queryParams.Get("sign"); sign != "" {
		strmData.Sign = sign
	}
	strmData.Path = ""
	if path := queryParams.Get("path"); path != "" {
		strmData.Path = path
	}
	strmData.BaseUrl = fmt.Sprintf("%s://%s", strmUrl.Scheme, strmUrl.Host)
	return &strmData
}

func (sf *SyncStrm) GetRemoteFilePathUrlEncode(path string) string {
	// 中文保留，只对特殊字符编码
	path = strings.ReplaceAll(path, "%", "%25")
	path = strings.ReplaceAll(path, "/", "%2F")
	path = strings.ReplaceAll(path, "?", "%3F")
	path = strings.ReplaceAll(path, "&", "%26")
	path = strings.ReplaceAll(path, "=", "%3D")
	path = strings.ReplaceAll(path, "+", "%2B")
	path = strings.ReplaceAll(path, "#", "%23")
	path = strings.ReplaceAll(path, "@", "%40")
	path = strings.ReplaceAll(path, "!", "%21")
	path = strings.ReplaceAll(path, "$", "%24")
	path = strings.ReplaceAll(path, " ", "%20")

	return path
}
