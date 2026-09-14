package emby

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/util/strs"

	"qmediasync/emby302/util/urls"
	"qmediasync/emby302/web/cache"
	"qmediasync/internal/helpers"
	"qmediasync/internal/playback"

	"github.com/gin-gonic/gin"
)

// Redirect2Transcode 将 master 请求重定向到本地 TS 代理
func Redirect2Transcode(c *gin.Context) {
	templateId := c.Query("template_id")
	if strs.AnyEmpty(templateId) {
		// 尝试从 MediaSourceId 中获取 TemplateId
		itemInfo, err := resolveItemInfo(c, RouteTranscode)
		if checkErr(c, err) {
			return
		}
		templateId = itemInfo.MsInfo.TemplateId
	}

	apiKey := c.Query(QueryApiKeyName)
	openlistPath := c.Query("openlist_path")
	if strs.AnyEmpty(templateId) {
		ProxyOrigin(c)
		return
	}

	// 只有 TemplateId 时, 需要先获取 OpenList Path
	if strs.AnyEmpty(openlistPath) {
		Redirect2OpenlistLink(c)
		return
	}

	tu, _ := url.Parse(https.ClientRequestHost(c.Request) + "/videos/proxy_playlist")
	q := tu.Query()
	q.Set("openlist_path", openlistPath)
	q.Set(QueryApiKeyName, apiKey)
	q.Set("template_id", templateId)
	tu.RawQuery = q.Encode()
	c.Redirect(http.StatusTemporaryRedirect, tu.String())
}

// Redirect2OpenlistLink 将资源重定向到 OpenList 网盘直链
func Redirect2OpenlistLink(c *gin.Context) {
	// 不处理字幕接口
	if strings.Contains(strings.ToLower(c.Request.RequestURI), "subtitles") {
		ProxyOrigin(c)
		return
	}

	// 1 解析要请求的资源信息
	itemInfo, err := resolveItemInfo(c, RouteStream)
	if checkErr(c, err) {
		return
	}
	// logs.Info("解析到的 itemInfo: %v", itemInfo)

	// 2 如果请求的是转码资源, 重定向到本地的 m3u8 代理服务
	msInfo := itemInfo.MsInfo
	useTranscode := !msInfo.Empty && msInfo.Transcode
	if useTranscode && msInfo.OpenlistPath != "" {
		u, _ := url.Parse(strings.ReplaceAll(MasterM3U8UrlTemplate, "${itemId}", itemInfo.Id))
		q := u.Query()
		q.Set("template_id", itemInfo.MsInfo.TemplateId)
		q.Set(QueryApiKeyName, itemInfo.ApiKey)
		q.Set("openlist_path", itemInfo.MsInfo.OpenlistPath)
		u.RawQuery = q.Encode()
		logs.Success("重定向播放列表: %s", u.String())
		c.Redirect(http.StatusTemporaryRedirect, u.String())
		return
	}

	// 3 请求资源在 Emby 中的 Path 参数
	embyPath, err := getEmbyFileLocalPath(itemInfo)
	if checkErr(c, err) {
		return
	}

	// logs.Info("检查 %s 是否为 NFS 协议的 STRM 文件", embyPath)
	strmUrl := ""
	// NFS 协议开头表示这是一个 NFS 文件路径, 打开该路径读取 STRM 内容, 再跳转到 STRM 内的地址。
	if strings.HasPrefix(embyPath, "nfs:") && strings.HasSuffix(embyPath, ".strm") {
		logs.Info("检测到 NFS 协议的 STRM 文件: %s", embyPath)
		// 打开 NFS 文件
		f, err := os.Open(embyPath)
		if err == nil {
			// 读取 STRM 内容
			buf := bytes.NewBufferString("")
			io.Copy(buf, f)
			strmUrl = buf.String()
			logs.Success("读取 STRM 文件成功：路径=%q", embyPath)
			f.Close()
		}
	}
	if strmUrl == "" {
		strmUrl = embyPath
	}
	isProxyUrl := ""
	// 4 如果是远程地址 (STRM) 且不包含 QMediaSync 的本地代理播放链接, 则重定向处理。
	if urls.IsRemote(strmUrl) || strings.HasPrefix(strmUrl, "http") || strings.HasPrefix(strmUrl, "nfs:") {
		finalPath, resolverStatus, expiresAt := getFinalRedirectLink(strmUrl, c.Request.Header.Clone())
		if !strings.Contains(finalPath, "/proxy-115") {
			targetHost := ""
			if target, err := url.Parse(finalPath); err == nil {
				targetHost = target.Hostname()
			}
			logs.Info("Emby STRM 跳转：文件=%q，UA=%q，接口状态=%d，跳转状态=%d，目标域名=%s",
				helpers.URLFileName(finalPath), c.Request.UserAgent(), resolverStatus, http.StatusTemporaryRedirect, targetHost)
			c.Header(cache.HeaderKeyExpired, "-1")
			if !expiresAt.IsZero() {
				c.Header(cache.HeaderKeyExpired, strconv.FormatInt(expiresAt.UnixMilli(), 10))
			}
			c.Redirect(http.StatusTemporaryRedirect, finalPath)
			return
		} else {
			logs.Warn("重定向 STRM 包含 QMediaSync 本地代理播放链接, 将回源处理（会走 NAS 流量）: %s", finalPath)
			isProxyUrl = finalPath
		}
	}

	// 5 如果是本地地址, 回源处理
	// 1. 以 / 开头
	// 2. 以 Windows 盘符开头, 通过正则匹配
	pattern := `^[A-Za-z]:`
	matchedWin, _ := regexp.MatchString(pattern, embyPath)
	// \\ 开头表示 Emby 网络共享地址
	if strings.HasPrefix(embyPath, "/") || matchedWin || strings.HasPrefix(embyPath, "\\") || isProxyUrl != "" {
		logs.Info("本地或代理路径: %s, 回源处理", embyPath)
		newUri := strings.Replace(c.Request.RequestURI, "stream", "original", 1)
		newUri = strings.Replace(newUri, "universal", "original", 1)
		c.Redirect(http.StatusTemporaryRedirect, newUri)
		return
	}
	// // 6 请求 OpenList 资源
	// fi := openlist.FetchInfo{
	// 	Header:       c.Request.Header.Clone(),
	// 	UseTranscode: useTranscode,
	// 	Format:       msInfo.TemplateId,
	// }
	// openlistPathRes := path.Emby2Openlist(embyPath)

	// allErrors := strings.Builder{}
	// // handleOpenlistResource 根据传入的 Path 请求 OpenList 资源
	// handleOpenlistResource := func(path string) bool {
	// 	logs.Info("尝试请求 Openlist 资源: %s", path)
	// 	fi.Path = path
	// 	res := openlist.FetchResource(fi)

	// 	if res.Code != http.StatusOK {
	// 		allErrors.WriteString(fmt.Sprintf("请求 OpenList 失败, 状态码: %d, 消息: %s, Path: %s;", res.Code, res.Msg, path))
	// 		return false
	// 	}

	// 	// 处理直链
	// 	if !fi.UseTranscode {
	// 		res.Data.Url = config.C.Emby.Strm.MapPath(res.Data.Url)
	// 		logs.Success("请求成功, 重定向到: %s", res.Data.Url)
	// 		c.Header(cache.HeaderKeyExpired, cache.Duration(time.Minute*10))
	// 		c.Redirect(http.StatusTemporaryRedirect, res.Data.Url)
	// 		return true
	// 	}

	// 	// 代理转码 m3u
	// 	u, _ := url.Parse(https.ClientRequestHost(c.Request) + "/videos/proxy_playlist")
	// 	q := u.Query()
	// 	q.Set("template_id", itemInfo.MsInfo.TemplateId)
	// 	q.Set(QueryApiKeyName, itemInfo.ApiKey)
	// 	q.Set("openlist_path", openlist.PathEncode(path))
	// 	u.RawQuery = q.Encode()
	// 	c.Redirect(http.StatusTemporaryRedirect, u.String())
	// 	return true
	// }

	// if openlistPathRes.Success && handleOpenlistResource(openlistPathRes.Path) {
	// 	return
	// }
	// paths, err := openlistPathRes.Range()
	// if checkErr(c, err) {
	// 	return
	// }
	// if slices.ContainsFunc(paths, func(path string) bool {
	// 	return handleOpenlistResource(path)
	// }) {
	// 	return
	// }

	checkErr(c, fmt.Errorf("没有兼容的流"))
}

// ProxyOriginalResource 拦截 original 接口
func ProxyOriginalResource(c *gin.Context) {
	// if strings.Contains(strings.ToLower(c.Request.RequestURI), "subtitles") {
	// 	ProxyOrigin(c)
	// 	return
	// }

	// itemInfo, err := resolveItemInfo(c, RouteOriginal)
	// if checkErr(c, err) {
	// 	return
	// }

	// embyPath, err := getEmbyFileLocalPath(itemInfo)
	// if checkErr(c, err) {
	// 	return
	// }

	// 如果是本地媒体, 代理回源
	// 2. 以 Windows 盘符开头, 通过正则匹配
	// pattern := `^[A-Za-z]:`
	// matchedWin, _ := regexp.MatchString(pattern, embyPath)
	// if strings.HasPrefix(embyPath, "/") || matchedWin || !strings.Contains(embyPath, "/proxy-115") {
	ProxyOrigin(c)
	// }
	// Redirect2OpenlistLink(c)
}

// checkErr 检查 err 是否为空
// 不为空则根据错误处理策略返回响应
//
// 返回 true 表示请求已经被处理
func checkErr(c *gin.Context, err error) bool {
	if err == nil || c == nil {
		return false
	}

	// 异常接口不缓存
	c.Header(cache.HeaderKeyExpired, "-1")

	// 采用拒绝策略, 直接返回错误
	if config.C.Emby.ProxyErrorStrategy == config.PeStrategyReject {
		logs.Error("代理接口失败: %v", err)
		c.String(http.StatusInternalServerError, "代理接口失败, 请检查日志")
		return true
	}

	logs.Error("代理接口失败: %v, 回源处理", err)
	ProxyOrigin(c)
	return true
}

// getFinalRedirectLink 请求 STRM 取链接口，返回跳转地址、取链响应状态和缓存截止时间。
//
// 自有 115 接口只读取 Location；其他服务沿用多跳解析。失败回退不缓存，未取得响应时状态为 0。
func getFinalRedirectLink(originLink string, header http.Header) (string, int, time.Time) {
	if origin, err := url.Parse(originLink); err == nil && origin.Host != "" &&
		(origin.Scheme == "http" || origin.Scheme == "https") &&
		(origin.Path == "/115/newurl" || strings.HasPrefix(origin.Path, "/115/url/")) {
		originLink = urls.AppendArgs(originLink, "force", "1")
		// 115 直链的 f=1 要求生成链接和后续 CDN 请求使用同一个 User-Agent，
		// 因此保留 STRM 请求头传给 QMS 取链接口，避免中间跳转丢失 UA。
		resp, err := https.Get(originLink).Header(header).DoSingle()
		responseStatus := 0
		if resp != nil {
			responseStatus = resp.StatusCode
			if resp.Body != nil {
				defer resp.Body.Close()
			}
		}
		if err != nil {
			logs.Warn("QMS 115 取链失败，回退原始链接：PickCode=%s，UA=%q，接口状态=%d，错误=%v",
				origin.Query().Get("pickcode"), header.Get("User-Agent"), responseStatus, helpers.URLRequestErrorForLog(err))
			return originLink, responseStatus, time.Time{}
		}
		if !https.IsRedirectCode(resp.StatusCode) && resp.StatusCode != http.StatusSeeOther {
			logs.Warn("QMS 115 取链失败，回退原始链接：PickCode=%s，UA=%q，接口状态=%d，原因=接口未返回跳转",
				origin.Query().Get("pickcode"), header.Get("User-Agent"), resp.StatusCode)
			return originLink, resp.StatusCode, time.Time{}
		}
		finalLink := resp.Header.Get("Location")
		finalURL, err := url.Parse(finalLink)
		if err != nil || finalURL.Hostname() == "" || (finalURL.Scheme != "http" && finalURL.Scheme != "https") {
			logs.Warn("QMS 115 取链失败，回退原始链接：PickCode=%s，UA=%q，接口状态=%d，原因=缺少或无效的 HTTP(S) Location",
				origin.Query().Get("pickcode"), header.Get("User-Agent"), resp.StatusCode)
			return originLink, resp.StatusCode, time.Time{}
		}
		return finalLink, resp.StatusCode, redirectCacheExpiresAt(finalLink, true, time.Now())
	}
	finalLink, resp, err := https.Get(originLink).Header(header).DoRedirect()
	if err != nil {
		logs.Warn("获取最终重定向链接失败, 原始链接: %s, 错误信息: %v", originLink, err)
		return originLink, 0, time.Time{}
	}
	logs.Success("获取最终重定向链接成功, 原始链接: %s, 最终链接: %s", originLink, finalLink)
	defer resp.Body.Close()
	if !https.IsSuccessCode(resp.StatusCode) {
		return finalLink, resp.StatusCode, time.Time{}
	}
	return finalLink, resp.StatusCode, redirectCacheExpiresAt(finalLink, false, time.Now())
}

// redirectCacheExpiresAt 对已识别的 115 链接严格要求签名期限，防止外层缓存跳过内层刷新。
func redirectCacheExpiresAt(rawURL string, qms115 bool, now time.Time) time.Time {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return time.Time{}
	}
	expiresAt := now.Add(10 * time.Minute)
	host := strings.ToLower(u.Hostname())
	if !qms115 && host != "115cdn.net" && !strings.HasSuffix(host, ".115cdn.net") {
		return expiresAt
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["t"]) != 1 {
		return time.Time{}
	}
	if expires, err := strconv.ParseInt(query.Get("t"), 10, 64); err != nil || expires <= 0 {
		return time.Time{}
	}
	signedExpiry := playback.URLExpiresAt(rawURL, now)
	if !signedExpiry.After(now) {
		return time.Time{}
	}
	if signedExpiry.Before(expiresAt) {
		expiresAt = signedExpiry
	}
	return expiresAt
}
