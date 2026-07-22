package emby

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/bytess"
	"qmediasync/emby302/util/encrypts"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/jsons"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/util/urls"

	"qmediasync/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

const (

	// ItemsCacheSpace 专门存放 Items 信息的缓存空间
	ItemsCacheSpace = "UserItems"

	// ResortMinNum 至少请求多少个 Item 才会走重排序逻辑
	ResortMinNum = 300

	// RandomItemsMaxLimit 随机 Items 回源与缓存允许的最大条目数
	RandomItemsMaxLimit = 500
)

// ResortRandomItems 对随机 Items 列表进行重排序
func ResortRandomItems(c *gin.Context) {
	// 如果没有开启配置, 代理原请求并返回
	if !config.C.Emby.ResortRandomItems {
		ProxyOrigin(c)
		return
	}

	// 如果请求的个数较少, 认为不是随机播放列表, 代理原请求并返回
	limit, err := strconv.Atoi(c.Query("Limit"))
	if err == nil && limit < ResortMinNum {
		ProxyOrigin(c)
		return
	}

	// 从缓存空间中获取列表
	spaceCache, ok := cache.GetSpaceCache(ItemsCacheSpace, calcRandomItemsCacheKey(c))

	// 缓存空间没有数据时, 默认使用 Emby 的原始随机结果
	if !ok {
		u := strings.ReplaceAll(c.Request.RequestURI, "/Items", "/Items/with_limit")
		c.Redirect(http.StatusTemporaryRedirect, u)
		return
	}

	bodyBytes := spaceCache.BodyBytes()
	code := spaceCache.Code()
	header := spaceCache.Headers()

	// 响应客户端。发生错误时, 回退使用原始 bodyBytes。
	err = nil
	var ih ItemsHolder
	defer func() {
		respBody, _ := json.Marshal(ih)
		if err != nil {
			logs.Error("随机排序接口返回非预期响应, 错误: %v, 已返回原始响应", err)
			respBody = bodyBytes
		}

		c.Status(code)
		header.Set("Content-Length", strconv.Itoa(len(respBody)))
		https.CloneHeader(c.Writer, header)
		c.Writer.Write(respBody)
	}()

	// 不关心 Item 内部结构, 直接使用原始 JSON 序列化结果提高处理速度
	if err = json.Unmarshal(bodyBytes, &ih); err != nil {
		return
	}

	itemLen := len(ih.Items)
	if itemLen == 0 {
		return
	}
	rand.Shuffle(itemLen, func(i, j int) {
		ih.Items[i], ih.Items[j] = ih.Items[j], ih.Items[i]
	})
}

// RandomItemsWithLimit 代理原始的随机列表接口
func RandomItemsWithLimit(c *gin.Context) {
	u := randomItemsUpstreamURL(c.Request.URL)
	embyHost := config.C.Emby.Host
	c.Request.Header.Del("Accept-Encoding")
	resp, err := https.Request(c.Request.Method, embyHost+u.String()).
		Header(c.Request.Header).
		Body(c.Request.Body).
		Do()
	if checkErr(c, err) {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		checkErr(c, fmt.Errorf("错误的响应码: %v", resp.StatusCode))
		return
	}

	c.Status(resp.StatusCode)
	https.CloneHeader(c.Writer, resp.Header)
	c.Header(cache.HeaderKeyExpired, cache.Duration(time.Hour*3))
	c.Header(cache.HeaderKeySpace, ItemsCacheSpace)
	c.Header(cache.HeaderKeySpaceKey, calcRandomItemsCacheKey(c))

	c.Writer.WriteHeaderNow()

	buf := bytess.CommonFixedBuffer()
	defer buf.PutBack()
	io.CopyBuffer(c.Writer, resp.Body, buf.Bytes())
}

// randomItemsUpstreamURL 构造随机 Items 的内部回源地址，不修改客户端请求 URL。
func randomItemsUpstreamURL(requestURL *url.URL) *url.URL {
	u := *requestURL
	u.Path = strings.TrimSuffix(u.Path, "/with_limit")

	q := u.Query()
	q.Set("Limit", randomItemsEffectiveLimit(q))
	q.Del("SortOrder")
	u.RawQuery = q.Encode()
	return &u
}

// randomItemsEffectiveLimit 返回随机 Items 实际回源时使用的 Limit。
func randomItemsEffectiveLimit(query url.Values) string {
	limit, err := strconv.ParseUint(strings.TrimSpace(query.Get("Limit")), 10, 64)
	if err != nil || limit > RandomItemsMaxLimit {
		return strconv.Itoa(RandomItemsMaxLimit)
	}
	return strconv.FormatUint(limit, 10)
}

// calcRandomItemsCacheKey 计算随机 Items 在缓存空间中的 key 值
func calcRandomItemsCacheKey(c *gin.Context) string {
	query := c.Request.URL.Query()
	_, _, apiKey := getApiKey(c)
	cacheIdentity := url.Values{
		"ApiKey":           {apiKey},
		"EnableImageTypes": {query.Get("EnableImageTypes")},
		"Fields":           {query.Get("Fields")},
		"ImageTypeLimit":   {query.Get("ImageTypeLimit")},
		"IncludeItemTypes": {query.Get("IncludeItemTypes")},
		"IsFavorite":       {query.Get("IsFavorite")},
		"IsFolder":         {query.Get("IsFolder")},
		"Limit":            {randomItemsEffectiveLimit(query)},
		"ParentId":         {query.Get("ParentId")},
		"ProjectToMedia":   {query.Get("ProjectToMedia")},
		"Recursive":        {query.Get("Recursive")},
	}
	return encrypts.Md5Hash(cacheIdentity.Encode())
}

// ProxyAddItemsPreviewInfo 代理 Items 接口, 并附带上转码版本信息
func ProxyAddItemsPreviewInfo(c *gin.Context) {
	// 代理请求
	c.Request.Header.Del("Accept-Encoding")
	resp, err := https.ProxyRequest(c.Request, config.C.Emby.Host)
	if checkErr(c, err) {
		return
	}
	defer resp.Body.Close()

	// 检查响应, 读取为 JSON
	if resp.StatusCode != http.StatusOK {
		checkErr(c, fmt.Errorf("Emby 远程返回错误响应码: %d", resp.StatusCode))
		return
	}
	resJson, err := jsons.Read(resp.Body)
	if checkErr(c, err) {
		return
	}

	// 预响应请求
	defer func() {
		https.CloneHeader(c.Writer, resp.Header)
		jsons.OkResp(c.Writer, resJson)
		go runtime.GC()
	}()

	// 获取 Items 数组
	itemsArr, ok := resJson.Attr("Items").Done()
	if !ok || itemsArr.Empty() || itemsArr.Type() != jsons.JsonTypeArr {
		return
	}

	// 遍历每个 Item, 修改 MediaSource 信息
	proresMediaStreams, _ := jsons.New(`[{"AspectRatio":"16:9","AttachmentSize":0,"AverageFrameRate":25,"BitDepth":8,"BitRate":4838626,"Codec":"prores","CodecTag":"hev1","DisplayTitle":"4K HEVC","ExtendedVideoSubType":"None","ExtendedVideoSubTypeDescription":"None","ExtendedVideoType":"None","Height":2160,"Index":0,"IsDefault":true,"IsExternal":false,"IsForced":false,"IsHearingImpaired":false,"IsInterlaced":false,"IsTextSubtitleStream":false,"Language":"und","Level":150,"PixelFormat":"yuv420p","Profile":"Main","Protocol":"File","RealFrameRate":25,"RefFrames":1,"SupportsExternalStream":false,"TimeBase":"1/90000","Type":"Video","VideoRange":"SDR","Width":3840},{"AttachmentSize":0,"BitRate":124573,"ChannelLayout":"stereo","Channels":2,"Codec":"aac","CodecTag":"mp4a","DisplayTitle":"AAC stereo (默认)","ExtendedVideoSubType":"None","ExtendedVideoSubTypeDescription":"None","ExtendedVideoType":"None","Index":1,"IsDefault":true,"IsExternal":false,"IsForced":false,"IsHearingImpaired":false,"IsInterlaced":false,"IsTextSubtitleStream":false,"Language":"und","Profile":"LC","Protocol":"File","SampleRate":44100,"SupportsExternalStream":false,"TimeBase":"1/44100","Type":"Audio"}]`)
	allTplIds := getAllPreviewTemplateIds()
	toAdd := make([]*jsons.Item, 0, len(allTplIds))
	itemsArr.RangeArr(func(index int, item *jsons.Item) error {
		mediaSources, ok := item.Attr("MediaSources").Done()
		if !ok || mediaSources.Empty() {
			return nil
		}

		toAdd = toAdd[:0]
		mediaSources.RangeArr(func(_ int, ms *jsons.Item) error {
			originId, _ := ms.Attr("Id").String()
			simplifyMediaName(ms)

			if path, ok := ms.Attr("Path").String(); ok {
				ms.Attr("Path").Set(urls.Unescape(path))
			}

			// 检查用户是否启用了转码版本获取
			if !config.C.VideoPreview.Enable {
				return nil
			}

			path, _ := ms.Attr("Path").Done()
			st, _ := ms.Attr("SupportsTranscoding").Done()
			t, _ := ms.Attr("Type").Done()
			rtt, _ := ms.Attr("RunTimeTicks").Done()
			sdp, _ := ms.Attr("SupportsDirectPlay").Done()
			itemId, _ := ms.Attr("ItemId").Done()
			sds, _ := ms.Attr("SupportsDirectStream").Done()
			ctn, _ := ms.Attr("Container").Done()

			originName, _ := ms.Attr("Name").String()
			for _, tplId := range allTplIds {
				copyMs := jsons.NewEmptyObj()
				copyMs.Put("Name", jsons.FromValue(fmt.Sprintf("(%s) %s", tplId, originName)))
				copyMs.Put("Id", jsons.FromValue(fmt.Sprintf("%s%s%s", originId, MediaSourceIdSegment, tplId)))
				copyMs.Put("MediaStreams", proresMediaStreams)
				copyMs.Put("Path", path)
				copyMs.Put("SupportsTranscoding", st)
				copyMs.Put("Type", t)
				copyMs.Put("RunTimeTicks", rtt)
				copyMs.Put("SupportsDirectPlay", sdp)
				copyMs.Put("ItemId", itemId)
				copyMs.Put("SupportsDirectStream", sds)
				copyMs.Put("Container", ctn)
				toAdd = append(toAdd, copyMs)
			}
			return nil
		})

		mediaSources.Append(toAdd...)
		return nil
	})
}

// ProxyLatestItems 代理 Latest 请求
func ProxyLatestItems(c *gin.Context) {
	// 代理请求
	c.Request.Header.Del("Accept-Encoding")
	resp, err := https.ProxyRequest(c.Request, config.C.Emby.Host)
	if checkErr(c, err) {
		return
	}
	defer resp.Body.Close()

	// 检查响应, 读取为 JSON
	if resp.StatusCode != http.StatusOK {
		checkErr(c, fmt.Errorf("Emby 远程返回错误响应码: %d", resp.StatusCode))
		return
	}
	resJson, err := jsons.Read(resp.Body)
	if checkErr(c, err) {
		return
	}

	// 预响应请求
	defer func() {
		https.CloneHeader(c.Writer, resp.Header)
		jsons.OkResp(c.Writer, resJson)
	}()

	// 遍历 MediaSources 并解码 Path
	if resJson.Type() != jsons.JsonTypeArr {
		return
	}
	resJson.RangeArr(func(_ int, item *jsons.Item) error {
		mediaSources, ok := item.Attr("MediaSources").Done()
		if !ok || mediaSources.Type() != jsons.JsonTypeArr || mediaSources.Empty() {
			return nil
		}
		mediaSources.RangeArr(func(_ int, ms *jsons.Item) error {
			if path, ok := ms.Attr("Path").String(); ok {
				ms.Attr("Path").Set(urls.Unescape(path))
			}
			return nil
		})
		return nil
	})

}
