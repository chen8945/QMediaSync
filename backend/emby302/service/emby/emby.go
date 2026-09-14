package emby

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"qmediasync/emby302/config"
	"qmediasync/emby302/util/bytess"
	"qmediasync/emby302/util/https"
	"qmediasync/emby302/util/jsons"
	"qmediasync/emby302/util/logs"
	"qmediasync/emby302/web/cache"

	"github.com/gin-gonic/gin"
)

func ProxySocket() func(*gin.Context) {

	var proxy *httputil.ReverseProxy
	var once = sync.Once{}

	initFunc := func() {
		origin := config.C.Emby.Host
		u, err := url.Parse(origin)
		if err != nil {
			panic("转换 Emby Host 失败: " + err.Error())
		}

		proxy = httputil.NewSingleHostReverseProxy(u)
		// WebSocket 直接连接 Emby，避免受系统代理环境变量影响。
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = nil
		proxy.Transport = transport

		proxy.Director = func(r *http.Request) {
			r.URL.Scheme = u.Scheme
			r.URL.Host = u.Host
		}
	}

	return func(c *gin.Context) {
		once.Do(initFunc)
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// HandleImages 处理图片请求
//
// 按配置请求原图或覆盖图片质量参数
// TODO 尝试跳转到 115 缩略图地址
// 根据 ItemId 查询 SyncFile, 如果有 115 缩略图地址就跳转过去
func HandleImages(c *gin.Context) {
	q := c.Request.URL.Query()
	if config.C.Emby.ImagesOriginal {
		// 参数名与 Emby 一样不区分大小写，保留选图、版本和认证参数。
		for key := range q {
			switch strings.ToLower(key) {
			case "maxwidth", "maxheight", "width", "height", "quality", "format",
				"cropwhitespace", "enableimageenhancers", "addplayedindicator", "percentplayed",
				"unplayedcount", "blur", "backgroundcolor", "foregroundlayer":
				q.Del(key)
			}
		}
		// Emby 缺省会裁剪 Logo/Art 并启用增强器，需要显式关闭。
		q.Set("CropWhitespace", "false")
		q.Set("EnableImageEnhancers", "false")
	} else {
		q.Del("quality")
		q.Del("Quality")
		q.Set("Quality", strconv.Itoa(config.C.Emby.ImagesQuality))
	}
	c.Request.RequestURI = c.Request.URL.Path + "?" + q.Encode()
	ProxyOrigin(c)
}

// ProxyOrigin 将请求代理到源服务器
func ProxyOrigin(c *gin.Context) {
	if c == nil {
		return
	}
	origin := config.C.Emby.Host

	// 传递客户端 IP 到 Emby
	c.Request.Header.Set("X-Forwarded-For", c.ClientIP())
	c.Request.Header.Set("X-Real-IP", c.ClientIP())

	if err := https.ProxyPass(c.Request, c.Writer, origin); err != nil {
		c.Header(cache.HeaderKeyExpired, "-1")
		logs.Error("代理异常: %v", err)
	}
}

// TestProxyUri 是用于测试的代理,
// 主要用于查看实际请求的详细信息
func TestProxyUri(c *gin.Context) bool {
	testUris := []string{}

	flag := false
	for _, uri := range testUris {
		if strings.Contains(c.Request.RequestURI, uri) {
			flag = true
			break
		}
	}
	if !flag {
		return false
	}

	type TestInfos struct {
		Uri        string
		Method     string
		Header     map[string]string
		Body       string
		RespStatus int
		RespHeader map[string]string
		RespBody   string
	}

	infos := &TestInfos{
		Uri:        c.Request.URL.String(),
		Method:     c.Request.Method,
		Header:     make(map[string]string),
		RespHeader: make(map[string]string),
	}

	for key, values := range c.Request.Header {
		infos.Header[key] = strings.Join(values, "|")
	}

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		logs.Error("测试 URI 执行异常: %v", err)
		return false
	}
	infos.Body = string(bodyBytes)

	origin := config.C.Emby.Host
	resp, err := https.Request(infos.Method, origin+infos.Uri).
		Header(c.Request.Header).
		Body(io.NopCloser(bytes.NewBuffer(bodyBytes))).
		Do()
	if err != nil {
		logs.Error("测试 URI 执行异常: %v", err)
		return false
	}
	defer resp.Body.Close()

	for key, values := range resp.Header {
		infos.RespHeader[key] = strings.Join(values, "|")
		for _, value := range values {
			c.Writer.Header().Add(key, value)
		}
	}

	bodyBytes, err = io.ReadAll(resp.Body)
	if err != nil {
		logs.Error("测试 URI 执行异常: %v", err)
		return false
	}
	infos.RespBody = string(bodyBytes)
	infos.RespStatus = resp.StatusCode
	logs.Warn("测试 URI 代理信息: %s", jsons.FromValue(infos))

	c.Status(infos.RespStatus)
	c.Writer.Write(bodyBytes)

	return true
}

// ProxyRoot 代理 Web 首页
func ProxyRoot(c *gin.Context) {
	resp, err := https.Request(c.Request.Method, config.C.Emby.Host+c.Request.URL.String()).
		Header(c.Request.Header).
		Body(c.Request.Body).
		DoSingle()

	if checkErr(c, err) {
		return
	}
	defer resp.Body.Close()

	https.CloneHeader(c.Writer, resp.Header)
	c.Status(resp.StatusCode)

	buf := bytess.CommonFixedBuffer()
	defer buf.PutBack()
	io.CopyBuffer(c.Writer, resp.Body, buf.Bytes())
}
