package m3u8

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/openlist"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/util/urls"
)

// NewByContent 根据 M3U8 文本初始化一个 Info 对象
//
// 如果文本中的 TS 地址是相对地址, 可通过 baseUrl 指定请求前缀。
func NewByContent(baseUrl string, content io.Reader) (*Info, error) {
	info := Info{
		RemoteBase:    baseUrl,
		RemoteTsInfos: make([]*TsInfo, 0, 1<<7),
		HeadComments:  make([]string, 0, 1<<3),
		TailComments:  make([]string, 0, 1<<3),
	}

	// 逐行遍历文本
	scanner := bufio.NewScanner(content)
	lineComments := make([]string, 0, 1<<3)
	for scanner.Scan() {
		lineBytes := scanner.Bytes()
		if len(lineBytes) == 0 {
			continue
		}

		// 1 扫描到一行 TS
		if lineBytes[0] != '#' {
			lineBytes, found := bytes.CutPrefix(lineBytes, []byte(baseUrl))
			if found {
				lineBytes = bytes.TrimLeft(lineBytes, "/")
			}
			tsInfo := TsInfo{Url: string(lineBytes), Comments: append([]string(nil), lineComments...)}
			info.RemoteTsInfos = append(info.RemoteTsInfos, &tsInfo)
			lineComments = lineComments[:0]
			continue
		}

		// 2 扫描到注释
		segIdx := bytes.IndexByte(lineBytes, ':')
		if segIdx == -1 {
			segIdx = len(lineBytes)
		}

		prefix := string(lineBytes[:segIdx])
		if _, ok := ParentHeadComments[prefix]; ok {
			info.HeadComments = append(info.HeadComments, string(lineBytes))
			continue
		}
		if _, ok := ParentTailComments[prefix]; ok {
			info.TailComments = append(info.TailComments, string(lineBytes))
			continue
		}
		lineComments = append(lineComments, string(lineBytes))
	}

	if scanner.Err() != nil {
		return nil, scanner.Err()
	}

	return &info, nil
}

// NewByRemote 从一个远程 M3U8 地址中初始化 Info 对象
func NewByRemote(url string, header http.Header) (*Info, error) {
	// 1 解析 base 地址
	queryPos := strings.Index(url, "?")
	if queryPos == -1 {
		queryPos = len(url)
	}
	lastSepPos := strings.LastIndex(url[:queryPos], "/")
	if lastSepPos == -1 {
		return nil, fmt.Errorf("错误的 M3U8 地址: %s", url)
	}
	baseUrl := url[:lastSepPos+1]

	// 2 请求远程地址
	resp, err := https.Get(url).Header(header).Do()
	if err != nil {
		return nil, fmt.Errorf("请求远程地址失败, URL: %s, 错误: %v", url, err)
	}
	defer resp.Body.Close()

	// 3 判断是否为 M3U8 响应
	contentType := resp.Header.Get("Content-Type")
	if _, ok := ValidM3U8Contents[contentType]; !ok {
		return nil, fmt.Errorf("不是有效的 M3U8 响应: %s", contentType)
	}

	// 4 解析远程响应
	return NewByContent(baseUrl, resp.Body)
}

// GetTsLink 获取 TS 流的直链地址
func (i *Info) GetTsLink(idx int) (string, bool) {
	size := len(i.RemoteTsInfos)
	if idx < 0 || idx >= size {
		return "", false
	}
	return i.RemoteBase + i.RemoteTsInfos[idx].Url, true
}

// Deprecated: MasterFunc 获取变体 M3U8
//
// 当 Info 包含字幕时, 需要调用这个方法返回
func (i *Info) MasterFunc(cntMapper func() string, clientApiKey string) string {
	sb := strings.Builder{}
	sb.WriteString("#EXTM3U\n")
	sb.WriteString("#EXT-X-VERSION:3\n")
	// 写入字幕信息
	for _, subInfo := range i.Subtitles {
		u, _ := url.Parse("proxy_subtitle")
		q := u.Query()
		q.Set("openlist_path", openlist.PathEncode(i.OpenlistPath))
		q.Set("template_id", i.TemplateId)
		q.Set("sub_name", urls.ResolveResourceName(subInfo.Url))
		q.Set(emby.QueryApiKeyName, clientApiKey)
		u.RawQuery = q.Encode()
		cmt := fmt.Sprintf(`#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="subs",NAME="%s",LANGUAGE="%s",URI="%s"`, subInfo.Lang, subInfo.Lang, u.String())
		sb.WriteString(cmt + "\n")
	}
	sb.WriteString(`#EXT-X-STREAM-INF:SUBTITLES="subs"` + "\n")
	sb.WriteString(cntMapper())
	return sb.String()
}

// ContentFunc 将 i 对象转换成 M3U8 文本
//
// tsMapper 函数可以将当前 Info 中的 TS 地址映射为自定义地址。
// 两个参数分别是 TS 索引和地址值。
func (i *Info) ContentFunc(tsMapper func(int, string) string) string {
	sb := strings.Builder{}

	// 1 写头注释
	for _, cmt := range i.HeadComments {
		sb.WriteString(cmt + "\n")
	}

	// 2 写 TS
	for idx, ti := range i.RemoteTsInfos {
		for _, cmt := range ti.Comments {
			sb.WriteString(cmt + "\n")
		}
		sb.WriteString(tsMapper(idx, ti.Url) + "\n")
	}

	// 3 写尾注释
	for _, cmt := range i.TailComments {
		sb.WriteString(cmt + "\n")
	}

	res := strings.TrimSuffix(sb.String(), "\n")

	return res
}

// ProxyContent 将 i 转换为 M3U8 本地代理文本
func (i *Info) ProxyContent(main bool, routePrefix, clientApiKey string) string {
	baseRoute := strings.Builder{}
	if routePrefix != "" {
		baseRoute.WriteString(routePrefix)
		baseRoute.WriteString("/")
	}

	// 有内封字幕的资源, 切换为变体 M3U8
	if !main && len(i.Subtitles) > 0 {
		baseRoute.WriteString("proxy_playlist")
		return i.MasterFunc(func() string {
			u, _ := url.Parse(baseRoute.String())
			q := u.Query()
			q.Set("openlist_path", openlist.PathEncode(i.OpenlistPath))
			q.Set("template_id", i.TemplateId)
			q.Set(emby.QueryApiKeyName, clientApiKey)
			q.Set("type", "main")
			u.RawQuery = q.Encode()
			return u.String()
		}, clientApiKey)
	}

	baseRoute.WriteString("proxy_ts")
	return i.ContentFunc(func(idx int, _ string) string {
		u, _ := url.Parse(baseRoute.String())
		q := u.Query()
		q.Set("idx", strconv.Itoa(idx))
		q.Set("openlist_path", openlist.PathEncode(i.OpenlistPath))
		q.Set("template_id", i.TemplateId)
		q.Set(emby.QueryApiKeyName, clientApiKey)
		u.RawQuery = q.Encode()
		return u.String()
	})
}

// Content 将 i 转换为 M3U8 文本
func (i *Info) Content() string {
	return i.ContentFunc(func(_ int, url string) string {
		return i.RemoteBase + url
	})
}

// UpdateContent 从 OpenList 获取最新的 M3U8 并更新对象
//
// 通过 OpenlistPath 和 TemplateId 定位到唯一一个转码资源地址
func (i *Info) UpdateContent() error {
	if i.OpenlistPath == "" || i.TemplateId == "" {
		return errors.New("参数未设置, 无法更新")
	}
	logs.Progress("正在更新播放列表, OpenListPath: %s, TemplateId: %s", i.OpenlistPath, i.TemplateId)

	// 请求 OpenList 资源
	res := openlist.FetchResource(openlist.FetchInfo{
		Path:         i.OpenlistPath,
		UseTranscode: true,
		Format:       i.TemplateId,
	})
	if res.Code != http.StatusOK {
		return errors.New("请求 OpenList 失败: " + res.Msg)
	}

	// 解析地址
	newInfo, err := NewByRemote(res.Data.Url, nil)
	if err != nil {
		return fmt.Errorf("解析远程 M3U8 失败, URL: %s, 错误: %v", res.Data, err)
	}

	// 拷贝最新数据
	i.RemoteBase = newInfo.RemoteBase
	i.HeadComments = append(i.HeadComments[:0], newInfo.HeadComments...)
	i.TailComments = append(i.TailComments[:0], newInfo.TailComments...)
	i.RemoteTsInfos = append(i.RemoteTsInfos[:0], newInfo.RemoteTsInfos...)
	i.Subtitles = append(i.Subtitles[:0], res.Data.Subtitles...)
	i.LastUpdate = time.Now().UnixMilli()
	return nil
}
