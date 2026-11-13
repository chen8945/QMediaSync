package emby

import (
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strconv"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/jsons"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

const downloadRequestKey = "embyDownloadRequest"

// HandleSyncDownload 将同步下载项映射为普通下载请求，两次请求都检查真实媒体的下载权限。
func (upstream *Session) HandleSyncDownload(c *gin.Context) {
	jobItemID := filepath.Base(filepath.Dir(c.Request.URL.Path))
	targets, ok := upstream.fetchDownloadJSON(c, "/Sync/Targets", nil)
	if !ok {
		return
	}
	if targets.Type() != jsons.JsonTypeArr {
		abortDownload(c, http.StatusBadGateway, "Emby 同步目标响应异常")
		return
	}

	for index := 0; index < targets.Len(); index++ {
		targetID, valid := targets.Ti().Idx(index).Attr("Id").String()
		if !valid || targetID == "" {
			abortDownload(c, http.StatusBadGateway, "Emby 同步目标响应异常")
			return
		}
		ready, ok := upstream.fetchDownloadJSON(c, "/Sync/Items/Ready", url.Values{"TargetId": {targetID}})
		if !ok {
			return
		}
		if ready.Type() != jsons.JsonTypeArr {
			abortDownload(c, http.StatusBadGateway, "Emby 同步下载项响应异常")
			return
		}
		for readyIndex := 0; readyIndex < ready.Len(); readyIndex++ {
			entry := ready.Ti().Idx(readyIndex)
			id, valid := entry.Attr("SyncJobItemId").Int()
			if !valid {
				abortDownload(c, http.StatusBadGateway, "Emby 同步下载项响应异常")
				return
			}
			if strconv.Itoa(id) != jobItemID {
				continue
			}
			itemID, valid := ready.Ti().Idx(readyIndex).Attr("Item").Attr("Id").String()
			if !valid || itemID == "" {
				abortDownload(c, http.StatusBadGateway, "Emby 同步下载项缺少媒体 ID")
				return
			}
			mediaSourceID, valid := ready.Ti().Idx(readyIndex).Attr("Item").Attr("MediaSources").Idx(0).Attr("Id").String()
			if !valid || mediaSourceID == "" {
				abortDownload(c, http.StatusBadGateway, "Emby 同步下载项缺少媒体源 ID")
				return
			}
			if _, err := strconv.ParseUint(itemID, 10, 64); err != nil {
				abortDownload(c, http.StatusBadGateway, "Emby 同步下载项的媒体 ID 无效")
				return
			}
			if !upstream.checkDownloadPermission(c, itemID) {
				return
			}

			query := url.Values{"MediaSourceId": {mediaSourceID}}
			keyType, keyName, apiKey := getApiKey(c)
			if keyType == Query {
				query.Set(keyName, apiKey)
			}
			// 同源相对跳转保留 header 凭据，不能把完整 Authorization 当成 api_key。
			target := url.URL{Path: "/Items/" + itemID + "/Download", RawQuery: query.Encode()}
			c.Redirect(http.StatusTemporaryRedirect, target.String())
			return
		}
	}
	abortDownload(c, http.StatusNotFound, "找不到同步下载项")
}

// checkDownloadPermission 使用请求凭据读取条目级权限，不缓存允许结果。
func (upstream *Session) checkDownloadPermission(c *gin.Context, itemID string) bool {
	result, ok := upstream.fetchDownloadJSON(c, "/Items", url.Values{
		"Ids": {itemID}, "Fields": {"CanDownload"}, "Limit": {"1"},
	})
	if !ok {
		return false
	}
	items, ok := result.Attr("Items").Done()
	if !ok || items.Type() != jsons.JsonTypeArr {
		abortDownload(c, http.StatusBadGateway, "Emby 下载权限响应异常")
		return false
	}
	if items.Len() == 0 {
		abortDownload(c, http.StatusNotFound, "找不到下载媒体")
		return false
	}
	id, valid := items.Ti().Idx(0).Attr("Id").String()
	if items.Len() != 1 || !valid || id != itemID {
		abortDownload(c, http.StatusBadGateway, "Emby 下载权限响应的媒体 ID 不匹配")
		return false
	}
	allowed, valid := items.Ti().Idx(0).Attr("CanDownload").Bool()
	if !valid {
		abortDownload(c, http.StatusBadGateway, "Emby 未明确返回下载权限")
		return false
	}
	if !allowed {
		abortDownload(c, http.StatusForbidden, "没有该媒体的下载权限")
		return false
	}
	return true
}

// fetchDownloadJSON 严格检查源服务器状态；授权或同步映射失败不能走异常回源。
func (upstream *Session) fetchDownloadJSON(c *gin.Context, path string, query url.Values) (*jsons.Item, bool) {
	keyType, keyName, apiKey := getApiKey(c)
	if apiKey == "" {
		abortDownload(c, http.StatusUnauthorized, "鉴权失败")
		return nil, false
	}
	if query == nil {
		query = make(url.Values)
	}
	header := make(http.Header)
	if keyType == Query {
		query.Set(keyName, apiKey)
	} else {
		header.Set(keyName, apiKey)
	}
	u := url.URL{Path: path, RawQuery: query.Encode()}
	response, err := https.Get(upstream.Origin + u.String()).Client(upstream.Client).
		Context(c.Request.Context()).Header(header).DoSingle()
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		abortDownload(c, http.StatusBadGateway, "查询 Emby 下载信息失败")
		return nil, false
	}
	if response.StatusCode != http.StatusOK {
		status := response.StatusCode
		switch status {
		case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		default:
			status = http.StatusBadGateway
		}
		abortDownload(c, status, "查询 Emby 下载信息失败")
		return nil, false
	}
	result, err := jsons.Read(response.Body)
	if err != nil || c.Request.Context().Err() != nil {
		abortDownload(c, http.StatusBadGateway, "解析 Emby 下载信息失败")
		return nil, false
	}
	return result, true
}

func abortDownload(c *gin.Context, status int, message string) {
	c.Header(cache.HeaderKeyExpired, "-1")
	if c.Request.Method == http.MethodHead {
		c.AbortWithStatus(status)
		return
	}
	c.String(status, message)
	c.Abort()
}

// DownloadStrategyChecker 拦截下载请求，并在直链响应缓存之前检查权限。
func (upstream *Session) DownloadStrategyChecker() gin.HandlerFunc {
	downloadRoute := regexp.MustCompile(constant.Reg_ItemDownload)
	syncRoute := regexp.MustCompile(constant.Reg_ItemSyncDownload)
	return func(c *gin.Context) {
		cleanPath := path.Clean(c.Request.URL.Path)
		isDownload := downloadRoute.MatchString(cleanPath)
		isSyncDownload := syncRoute.MatchString(cleanPath)
		if !isDownload && !isSyncDownload {
			return
		}
		// Emby 接受重复和尾斜杠；解析同一规范路径，保留原始 RequestURI 供回源使用。
		c.Request.URL.Path = cleanPath
		strategy := config.C.Emby.DownloadStrategy
		if strategy == config.DlStrategyDirect {
			c.Set(downloadRequestKey, true)
			if isDownload {
				upstream.checkDownloadPermission(c, filepath.Base(filepath.Dir(c.Request.URL.Path)))
			} else {
				// 同步下载先完成授权，避免原始 URL 的编码或查询内容落入其他路由回源。
				upstream.HandleSyncDownload(c)
				c.Abort()
			}
			return
		}
		defer c.Abort()
		if strategy == config.DlStrategy403 {
			abortDownload(c, http.StatusForbidden, "下载接口已禁用")
			return
		}
		if strategy == config.DlStrategyOrigin {
			if err := https.ProxyPass(c.Request, c.Writer, upstream.Origin, upstream.Client); err != nil {
				c.Header(cache.HeaderKeyExpired, "-1")
				logs.Error("下载接口代理失败: %v", err)
			}
		}
	}
}
