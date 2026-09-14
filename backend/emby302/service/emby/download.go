package emby

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"

	"qmediasync/emby302/config"
	"qmediasync/emby302/constant"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/jsons"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

// HandleSyncDownload 处理 Sync 下载接口, 并重定向到直链
func HandleSyncDownload(c *gin.Context) {
	// 解析 JobItems ID
	itemInfo, err := resolveItemInfo(c, RouteSyncDownload)
	if checkErr(c, err) {
		return
	}
	logs.SensitiveDebug("解析得到的 ItemInfo 信息: %s", itemInfo.SensitiveString())
	if itemInfo.Id == "" {
		checkErr(c, errors.New("JobItems ID 为空"))
		return
	}

	// 请求 targets 列表
	targetUri := "/Sync/Targets?api_key=" + itemInfo.ApiKey
	resp, _ := Fetch(targetUri, http.MethodGet, nil, nil)
	if resp.Code != http.StatusOK {
		logs.SensitiveDebug("请求 Emby 失败完整 URI: %s", targetUri)
		checkErr(c, fmt.Errorf("请求 Emby 失败: %v, URI: %s", resp.Msg, targetUri))
		return
	}
	targets := resp.Data
	if targets.Empty() {
		checkErr(c, fmt.Errorf("targets 列表为空, 原始响应: %v", targets))
		return
	}

	// 逐个 ID 尝试
	readyUriTmpl := "/Sync/Items/Ready?api_key=" + itemInfo.ApiKey + "&TargetId="
	targets.RangeArr(func(_ int, target *jsons.Item) error {
		id, ok := target.Attr("Id").String()
		if !ok {
			return nil
		}

		// 请求 Ready 接口
		readyUri := readyUriTmpl + id
		resp, _ := Fetch(readyUri, http.MethodGet, nil, nil)
		if resp.Code != http.StatusOK {
			logs.SensitiveDebug("请求 Emby 失败完整 URI: %s", readyUri)
			checkErr(c, fmt.Errorf("请求 Emby 失败: %v, URI: %s", resp.Msg, readyUri))
			return jsons.ErrBreakRange
		}
		readyItems := resp.Data
		if readyItems.Empty() {
			return nil
		}

		// 遍历所有 Item
		breakRange := false
		readyItems.RangeArr(func(_ int, ri *jsons.Item) error {
			jobId, ok := ri.Attr("SyncJobItemId").Int()
			if !ok {
				return nil
			}
			if strconv.Itoa(jobId) != itemInfo.Id {
				return nil
			}

			// 匹配成功后获取下载项目的 ItemId, 重新封装请求并重定向到直链
			itemId, ok := ri.Attr("Item").Attr("Id").String()
			if !ok {
				checkErr(c, fmt.Errorf("解析 Emby 响应失败: 获取不到 ItemId, 原始响应: %v", ri))
				breakRange = true
				return jsons.ErrBreakRange
			}
			msId, ok := ri.Attr("Item").Attr("MediaSources").Idx(0).Attr("Id").String()
			if !ok {
				checkErr(c, fmt.Errorf("解析 Emby 响应失败: 获取不到 MediaSourceId, 原始响应: %v", ri))
				breakRange = true
				return jsons.ErrBreakRange
			}
			logs.Success("已匹配到 ItemId: %s, MediaSourceId: %s", itemId, msId)

			newUrl, _ := url.Parse(fmt.Sprintf("/videos/%s/stream?MediaSourceId=%s&api_key=%s&Static=true", itemId, msId, itemInfo.ApiKey))
			c.Redirect(http.StatusTemporaryRedirect, newUrl.String())
			breakRange = true
			return jsons.ErrBreakRange
		})

		if breakRange {
			return jsons.ErrBreakRange
		}

		return nil
	})

}

// DownloadStrategyChecker 拦截下载请求, 并根据配置的策略进行响应
func DownloadStrategyChecker() gin.HandlerFunc {

	var downloadRoutes = []*regexp.Regexp{
		regexp.MustCompile(constant.Reg_ItemDownload),
		regexp.MustCompile(constant.Reg_ItemSyncDownload),
	}

	return func(c *gin.Context) {
		// 放行非下载接口
		var flag bool
		for _, route := range downloadRoutes {
			if route.MatchString(c.Request.RequestURI) {
				flag = true
				break
			}
		}
		if !flag {
			return
		}

		strategy := config.C.Emby.DownloadStrategy

		if strategy == config.DlStrategyDirect {
			return
		}
		defer c.Abort()

		if strategy == config.DlStrategy403 {
			c.String(http.StatusForbidden, "下载接口已禁用")
			return
		}

		if strategy == config.DlStrategyOrigin {
			if err := https.ProxyPass(c.Request, c.Writer, config.C.Emby.Host); err != nil {
				c.Header(cache.HeaderKeyExpired, "-1")
				logs.Error("下载接口代理失败: %v", err)
			}
		}

	}
}
