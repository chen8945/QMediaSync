package cache

import (
	"bytes"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"qmediasync/emby302/constant"
	"qmediasync/emby302/util/encrypts"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/logs"

	"github.com/gin-gonic/gin"
)

// CacheKeyIgnoreParams 忽略的请求头或参数
//
// 如果请求地址包含列表中的请求头或参数, 则不参与 cache key 运算。
var CacheKeyIgnoreParams = map[string]struct{}{
	// Fileball
	"StartTimeTicks": {}, "X-Playback-Session-Id": {},

	// Emby
	"PlaySessionId": {},

	// Common
	"Range": {}, "Host": {}, "Referrer": {}, "Connection": {},
	"Accept": {}, "Accept-Encoding": {}, "Accept-Language": {}, "Cache-Control": {},
	"Upgrade-Insecure-Requests": {}, "Referer": {}, "Origin": {},

	// StreamMusic
	"X-Streammusic-Audioid": {}, "X-Streammusic-Savepath": {},

	// IP
	"X-Forwarded-For": {}, "X-Real-IP": {}, "X-Real-Ip": {}, "Forwarded": {}, "Client-IP": {},
	"True-Client-IP": {}, "CF-Connecting-IP": {}, "X-Cluster-Client-IP": {},
	"Fastly-Client-IP": {}, "X-Client-IP": {}, "X-ProxyUser-IP": {},
	"Via": {}, "Forwarded-For": {}, "X-From-Cdn": {},
}

// CacheableRouteMarker 缓存白名单
// 只有匹配上正则表达式的路由才会被缓存
func CacheableRouteMarker() gin.HandlerFunc {
	cacheablePatterns := []*regexp.Regexp{
		regexp.MustCompile(constant.Reg_PlaybackInfo),
		regexp.MustCompile(constant.Reg_VideoSubtitles),
		regexp.MustCompile(constant.Reg_ResourceStream),
		regexp.MustCompile(constant.Reg_ItemDownload),
		regexp.MustCompile(constant.Reg_ItemSyncDownload),
		regexp.MustCompile(constant.Reg_UserItemsRandomWithLimit),
	}

	return func(c *gin.Context) {
		for _, pattern := range cacheablePatterns {
			if pattern.MatchString(c.Request.RequestURI) {
				return
			}
		}
		c.Header(HeaderKeyExpired, "-1")
	}
}

// RequestCacher 请求缓存中间件
func RequestCacher() gin.HandlerFunc {
	return func(c *gin.Context) {
		// 1 判断请求是否需要缓存
		if c.Writer.Header().Get(HeaderKeyExpired) == "-1" {
			return
		}

		// 2 计算 cache key
		cacheKey, err := calcCacheKey(c)
		if err != nil {
			logs.Warn("计算 cache key 失败: %v, 跳过缓存", err)
			// 如果没有调用 Abort, Gin 会自动继续调用处理器链
			return
		}

		// 3 尝试获取缓存
		if rc, ok := getCache(cacheKey); ok {
			rc.mu.RLock()
			code := rc.code
			header := rc.header.header.Clone()
			body := append([]byte(nil), rc.body...)
			rc.mu.RUnlock()
			if https.IsRedirectCode(code) {
				// 适配重定向请求
				c.Redirect(code, header.Get("Location"))
			} else {
				c.Status(code)
				https.CloneHeader(c.Writer, header)
				c.Writer.Write(body)
			}
			c.Abort()
			return
		}

		// 4 使用自定义的响应器
		customWriter := &respCacheWriter{body: &bytes.Buffer{}, ResponseWriter: c.Writer}
		c.Writer = customWriter

		// 5 执行请求处理器
		c.Next()

		// 6 不缓存错误请求
		if https.IsErrorStatus(c.Writer.Status()) {
			return
		}

		// 7 刷新缓存
		header := c.Writer.Header()
		respHeader := respHeader{
			expired:  header.Get(HeaderKeyExpired),
			space:    header.Get(HeaderKeySpace),
			spaceKey: header.Get(HeaderKeySpaceKey),
			header:   header.Clone(),
		}
		defer header.Del(HeaderKeyExpired)
		defer header.Del(HeaderKeySpace)
		defer header.Del(HeaderKeySpaceKey)

		// 响应快照同步入队，后台维护不再访问会被 Gin 复用的 Context。
		putCache(cacheKey, c.Writer.Status(), append([]byte(nil), customWriter.body.Bytes()...), respHeader)
	}
}

// Duration 将标准时间转换成适用于缓存时间的字符串
func Duration(d time.Duration) string {
	expired := d.Milliseconds() + time.Now().UnixMilli()
	return fmt.Sprintf("%v", expired)
}

// WaitingForHandleChan 等待预缓存通道被处理完毕
func WaitingForHandleChan() {
	cacheHandleWaitGroup.Wait()
}

// calcCacheKey 计算缓存 key
//
// 请求方法、URL、请求体及请求头使用长度前缀编码后进行 MD5 哈希。
// 仅排序参数名和请求头名称，保留字段值及多值顺序，不修改转发请求。
func calcCacheKey(c *gin.Context) (string, error) {
	u := *c.Request.URL
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", fmt.Errorf("解析请求参数失败: %w", err)
	}
	for key := range CacheKeyIgnoreParams {
		q.Del(key)
	}
	u.RawQuery = q.Encode()

	body := ""
	if c.Request.Body != nil {
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return "", fmt.Errorf("读取请求体失败: %v", err)
		}
		body = string(bodyBytes)
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	}
	identity := strings.Builder{}
	writeField := func(value string) {
		fmt.Fprintf(&identity, "%d:", len(value))
		identity.WriteString(value)
	}
	writeField(c.Request.Method)
	writeField(u.String())
	writeField(body)

	headerKeys := make([]string, 0, len(c.Request.Header))
	for key := range c.Request.Header {
		if _, ok := CacheKeyIgnoreParams[key]; !ok {
			headerKeys = append(headerKeys, key)
		}
	}
	sort.Strings(headerKeys)
	fmt.Fprintf(&identity, "%d:", len(headerKeys))
	header := strings.Builder{}
	for _, key := range headerKeys {
		values := c.Request.Header[key]
		writeField(key)
		fmt.Fprintf(&identity, "%d:", len(values))
		for _, value := range values {
			writeField(value)
		}
		header.WriteString(key)
		header.WriteString("=")
		header.WriteString(strings.Join(values, "|"))
		header.WriteString(";")
	}

	headerStr := header.String()
	if headerStr != "" {
		logs.Tip("参与 cache key 计算的请求头: %s", headerStr)
	}

	return encrypts.Md5Hash(identity.String()), nil
}
