package emby

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

// ResortEpisodes 代理剧集列表请求
//
// 如果开启了 emby.episodes-unplay-prior 配置,
// 则将未播剧集排在前面
func ResortEpisodes(c *gin.Context) {
	// 1 检查配置是否开启
	if !config.C.Emby.EpisodesUnplayPrior {
		err := https.ProxyPass(c.Request, c.Writer, config.C.Emby.Host)
		if err != nil && c.Writer.Written() {
			// 已开始的响应不能再回源，否则会拼接两份内容。
			c.Header(cache.HeaderKeyExpired, "-1")
			logs.Error("剧集接口代理失败: %v", err)
			return
		}
		checkErr(c, err)
		return
	}

	// 2 去除分页限制
	q := c.Request.URL.Query()
	q.Del("Limit")
	q.Del("StartIndex")
	var querySuffix string
	if len(q) > 0 {
		querySuffix = "?" + q.Encode()
	}
	c.Request.RequestURI = c.Request.URL.Path + querySuffix

	// 3 代理请求
	c.Request.Header.Del("Accept-Encoding")
	resp, err := https.ProxyRequest(c.Request, config.C.Emby.Host)
	if checkErr(c, err) {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		checkErr(c, errors.New(resp.Status))
		return
	}

	// 4 处理数据
	bodyBytes, err := io.ReadAll(resp.Body)
	if checkErr(c, err) {
		return
	}
	var ih ItemsHolder
	if err = json.Unmarshal(bodyBytes, &ih); checkErr(c, err) {
		return
	}
	resp.Header.Del("Content-Length")
	https.CloneHeader(c.Writer, resp.Header)
	defer func() {
		bytes, _ := json.Marshal(ih)
		c.Header("Content-Length", strconv.Itoa(len(bytes)))
		c.Writer.WriteHeaderNow()
		c.Writer.Write(bytes)
	}()

	if len(ih.Items) == 0 {
		return
	}

	type ValueInner struct {
		UserData struct {
			Played bool
		}
	}
	playedItems, allItems := make([]json.RawMessage, 0, len(ih.Items)), make([]json.RawMessage, 0, len(ih.Items))
	for idx, value := range ih.Items {
		if len(allItems) > 0 {
			// 找到第一个未播剧集之后, 剩余剧集都按未播处理
			allItems = slices.Concat(allItems, ih.Items[idx:])
			break
		}

		var vi ValueInner
		if err := json.Unmarshal(value, &vi); err != nil {
			allItems = append(allItems, value)
			continue
		}

		if vi.UserData.Played {
			playedItems = append(playedItems, value)
			continue
		}
		allItems = append(allItems, value)
	}

	// 将已播的数据放在末尾
	allItems = append(allItems, playedItems...)
	ih.Items = allItems
}
