package emby

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/util/https"
	"qmediasync/internal/emby302/util/logs"

	"github.com/gin-gonic/gin"
	"golang.org/x/sync/errgroup"
)

// CustomAssets 在模块内共享启动只读脚本，加载工作随模块取消。
type CustomAssets struct {
	js, css []string
	load    func()
}

// NewCustomAssets 绑定模块客户端和取消边界，首次请求才加载静态资源。
func NewCustomAssets(ctx context.Context, client *http.Client, basePath string) *CustomAssets {
	assets := &CustomAssets{}
	assets.load = sync.OnceFunc(func() {
		loadRemoteContent := func(originBytes []byte) ([]byte, error) {
			if len(originBytes) == 0 {
				return []byte{}, nil
			}

			str := strings.TrimSpace(string(originBytes))
			u, err := url.Parse(str)
			if err != nil {
				// 非远程地址
				return originBytes, nil
			}

			resp, err := https.Get(u.String()).Client(client).Context(ctx).Do()
			if err != nil {
				return nil, fmt.Errorf("远程加载失败: %s, 错误: %v", u.String(), err)
			}
			defer resp.Body.Close()
			if !https.IsSuccessCode(resp.StatusCode) {
				return nil, fmt.Errorf("远程返回错误响应: %s, 状态: %s", u.String(), resp.Status)
			}

			bytes, err := io.ReadAll(resp.Body)
			if err != nil {
				return nil, fmt.Errorf("读取远程内容失败: %s, 错误: %v", u.String(), err)
			}

			return bytes, nil
		}

		loadFiles := func(fp, ext, successLogPrefix string) ([]string, error) {
			if err := os.MkdirAll(fp, os.ModePerm); err != nil {
				return nil, fmt.Errorf("初始化目录失败: %s, 错误: %v", fp, err)
			}

			files, err := os.ReadDir(fp)
			if err != nil {
				return nil, fmt.Errorf("读取目录失败: %s, 错误: %v, 无法注入自定义脚本", fp, err)
			}

			res := []string{}
			ch := make(chan string)
			chg := new(errgroup.Group)
			chg.Go(func() error {
				for content := range ch {
					res = append(res, content)
				}
				return nil
			})

			g := new(errgroup.Group)
			for _, file := range files {
				if file.IsDir() {
					continue
				}

				if filepath.Ext(file.Name()) != ext {
					continue
				}

				g.Go(func() error {
					content, err := os.ReadFile(filepath.Join(fp, file.Name()))
					if err != nil {
						return fmt.Errorf("读取文件失败: %s, 错误: %v", file.Name(), err)
					}

					// 支持远程加载
					content, err = loadRemoteContent(content)
					if err != nil {
						return fmt.Errorf("远程加载失败: %s, 错误: %v", file.Name(), err)
					}

					ch <- string(content)
					logs.Success("%s已加载: %s", successLogPrefix, file.Name())
					return nil
				})

			}

			err = g.Wait()
			close(ch)
			_ = chg.Wait()
			return res, err
		}

		fp := filepath.Join(basePath, constant.CustomJsDirName)
		jsList, err := loadFiles(fp, ".js", "自定义脚本")
		if err != nil {
			logs.Error("加载自定义脚本失败: %v", err)
			return
		}
		assets.js = jsList

		fp = filepath.Join(basePath, constant.CustomCssDirName)
		cssList, err := loadFiles(fp, ".css", "自定义样式表")
		if err != nil {
			logs.Error("加载自定义样式表失败: %v", err)
			return
		}
		assets.css = cssList
	})
	return assets
}

// ProxyIndexHtml 代理 index.html, 注入自定义脚本和样式文件
func (upstream *Session) ProxyIndexHtml(c *gin.Context) {
	resp, err := https.ProxyRequest(c.Request, upstream.Origin, upstream.Client)
	if upstream.checkErr(c, err) {
		return
	}
	defer resp.Body.Close()

	if !https.IsSuccessCode(resp.StatusCode) {
		upstream.checkErr(c, fmt.Errorf("远程返回错误响应: %v", resp.Status))
		return
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if upstream.checkErr(c, err) {
		return
	}

	content := string(bodyBytes)
	bodyCloseTag := "</body>"
	customJsElm := fmt.Sprintf(`    <script src="%s"></script>`, constant.Route_CustomJs)
	content = strings.ReplaceAll(content, bodyCloseTag, customJsElm+"\n"+bodyCloseTag)

	customCssElm := fmt.Sprintf(`    <link rel="stylesheet" type="text/css" href="%s">`, constant.Route_CustomCss)
	content = strings.ReplaceAll(content, bodyCloseTag, customCssElm+"\n"+bodyCloseTag)

	c.Status(resp.StatusCode)
	resp.Header.Del("Content-Length")
	https.CloneHeader(c.Writer, resp.Header)
	c.Writer.Write([]byte(content))
	c.Writer.Flush()
}

// ProxyCustomJs 代理自定义脚本，等待 ApiClient 初始化后执行。
func (upstream *Session) ProxyCustomJs(c *gin.Context) {
	upstream.assets.load()

	contentBuilder := strings.Builder{}
	for _, script := range upstream.assets.js {
		fmt.Fprintf(&contentBuilder, `(function waitForEmby() {
  if (typeof ApiClient === 'undefined' || ApiClient === null) {
    setTimeout(waitForEmby, 100);
    return;
  }
  try {
    (function() {
%s
    })();
  } catch (e) {
    console.error('自定义脚本执行失败:', e);
  }
})();
`, script)
	}
	contentBytes := []byte(contentBuilder.String())

	c.Status(http.StatusOK)
	c.Header("Content-Type", "application/javascript")
	c.Header("Content-Length", fmt.Sprintf("%d", len(contentBytes)))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Writer.Write(contentBytes)
	c.Writer.Flush()
}

// ProxyCustomCss 代理自定义样式表
func (upstream *Session) ProxyCustomCss(c *gin.Context) {
	upstream.assets.load()

	contentBuilder := strings.Builder{}
	for _, style := range upstream.assets.css {
		contentBuilder.WriteString(fmt.Sprintf("%s\n\n\n", style))
	}
	contentBytes := []byte(contentBuilder.String())

	c.Status(http.StatusOK)
	c.Header("Content-Type", "text/css")
	c.Header("Content-Length", fmt.Sprintf("%d", len(contentBytes)))
	c.Header("Cache-Control", "no-store")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Writer.Write(contentBytes)
	c.Writer.Flush()
}
