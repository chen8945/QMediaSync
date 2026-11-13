package emby

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http/httputil"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"qmediasync/internal/emby302/config"
	"qmediasync/internal/emby302/util/bytess"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/jsons"
	"qmediasync/internal/emby302/util/logs"
	"qmediasync/internal/emby302/web/cache"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/http/httpguts"
)

type runtimeRequestReleaseKey struct{}

// WithRuntimeRequestRelease 把本次请求许可带到完成数据库访问的 WebSocket 路由。
func WithRuntimeRequestRelease(ctx context.Context, release func()) context.Context {
	return context.WithValue(ctx, runtimeRequestReleaseKey{}, release)
}

type socketResponseWriter struct {
	gin.ResponseWriter
	release func()
}

func (w *socketResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, buffered, err := w.ResponseWriter.Hijack()
	if err == nil {
		// 升级后只转发连接数据；Session 引用仍保留到连接结束。
		w.release()
	}
	return conn, buffered, err
}

func (upstream *Session) ProxySocket() func(*gin.Context) {

	var proxy *httputil.ReverseProxy

	initFunc := func() {
		origin := upstream.Origin
		u, err := url.Parse(origin)
		if err != nil {
			panic("转换 Emby Host 失败: " + err.Error())
		}

		proxy = &httputil.ReverseProxy{
			Transport: upstream.Client.Transport,
			Rewrite: func(r *httputil.ProxyRequest) {
				r.Out.URL.Scheme = u.Scheme
				r.Out.URL.Host = u.Host
				// 保留原始路径、Host 和查询串，避免重编码认证参数。
				r.Out.URL.RawQuery = r.In.URL.RawQuery
				// 沿用既有转发头，但不能恢复被 Connection 指定为逐跳的字段。
				for _, name := range []string{"Forwarded", "X-Forwarded-Host", "X-Forwarded-Proto", "X-Forwarded-For"} {
					if values, ok := r.In.Header[name]; ok && !httpguts.HeaderValuesContainsToken(r.In.Header.Values("Connection"), name) {
						r.Out.Header[name] = slices.Clone(values)
					}
				}
				if clientIP, _, err := net.SplitHostPort(r.In.RemoteAddr); err == nil {
					prior, ok := r.Out.Header["X-Forwarded-For"]
					if !ok || prior != nil {
						if len(prior) > 0 {
							clientIP = strings.Join(prior, ", ") + ", " + clientIP
						}
						r.Out.Header.Set("X-Forwarded-For", clientIP)
					}
				}
			},
		}
	}

	initFunc()
	return func(c *gin.Context) {
		if release, ok := c.Request.Context().Value(runtimeRequestReleaseKey{}).(func()); ok {
			c.Writer = &socketResponseWriter{ResponseWriter: c.Writer, release: release}
		}
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

// HandleImages 处理图片请求
//
// 按配置请求原图或覆盖图片质量参数
// TODO 尝试跳转到 115 缩略图地址
// 根据 ItemId 查询 SyncFile, 如果有 115 缩略图地址就跳转过去
func (upstream *Session) HandleImages(c *gin.Context) {
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
	upstream.ProxyOrigin(c)
}

// ProxyOrigin 将请求代理到源服务器
func (upstream *Session) ProxyOrigin(c *gin.Context) {
	if c == nil {
		return
	}
	origin := upstream.Origin

	// 传递客户端 IP 到 Emby
	c.Request.Header.Set("X-Forwarded-For", c.ClientIP())
	c.Request.Header.Set("X-Real-IP", c.ClientIP())

	if err := https.ProxyPass(c.Request, c.Writer, origin, upstream.Client); err != nil {
		c.Header(cache.HeaderKeyExpired, "-1")
		if errors.Is(err, context.Canceled) && errors.Is(c.Request.Context().Err(), context.Canceled) {
			logs.Debug("客户端已取消 Emby 请求：接口=%s", c.Request.URL.Path)
		} else {
			logs.Error("代理异常: %v", err)
		}
	}
}

// TestProxyUri 是用于测试的代理,
// 主要用于查看实际请求的详细信息
func (upstream *Session) TestProxyUri(c *gin.Context) bool {
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

	origin := upstream.Origin
	resp, err := https.Request(infos.Method, origin+infos.Uri).Client(upstream.Client).Context(c.Request.Context()).
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
func (upstream *Session) ProxyRoot(c *gin.Context) {
	resp, err := https.Request(c.Request.Method, upstream.Origin+c.Request.URL.String()).Client(upstream.Client).Context(c.Request.Context()).
		Header(c.Request.Header).
		Body(c.Request.Body).
		DoSingle()

	if upstream.checkErr(c, err) {
		return
	}
	defer resp.Body.Close()

	https.CloneHeader(c.Writer, resp.Header)
	c.Status(resp.StatusCode)

	buf := bytess.CommonFixedBuffer()
	defer buf.PutBack()
	io.CopyBuffer(c.Writer, resp.Body, buf.Bytes())
}
