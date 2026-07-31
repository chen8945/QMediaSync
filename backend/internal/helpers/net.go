package helpers

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"qmediasync/internal/github"
	"qmediasync/internal/validation"
)

// 获取本机网卡 IP
func GetLocalIP() (ipv4 string, err error) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return
	}
	// 取第一个非 lo 的网卡 IP
	for _, addr := range addrs {
		ipNet, isIpNet := addr.(*net.IPNet)
		if isIpNet && !ipNet.IP.IsLoopback() {
			if ipNet.IP.To4() != nil {
				ipv4 = ipNet.IP.String()
				return
			}
		}
	}
	return
}

// 打开 URL 并读取内容
func ReadFromUrl(targetUrl string, userAgent string) (content []byte, err error) {
	// 创建请求并设置 User-Agent
	req, err := http.NewRequest("GET", targetUrl, nil)
	if err != nil {
		AppLogger.Errorf("[下载] 创建 %s 的 HTTP 请求失败：%v", targetUrl, err)
		return nil, fmt.Errorf("创建 %s 的 HTTP 请求失败：%v", targetUrl, err)
	}
	req.Header.Set("User-Agent", userAgent)

	// 创建传输对象并配置代理
	transport := &http.Transport{}

	// // 设置代理
	// proxyURL := "http://127.0.0.1:10808"
	// proxy, perr := url.Parse(proxyURL)
	// if perr != nil {
	// 	AppLogger.Warnf("[下载] 解析代理 URL 失败：%v", perr)
	// } else {
	// 	transport.Proxy = http.ProxyURL(proxy)
	// 	AppLogger.Infof("[下载] 使用代理：%s", proxyURL)
	// }

	// 发送请求 - 禁用自动重定向，改为手动处理
	client := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		// 禁用自动重定向，返回最后一个响应
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		// AppLogger.Errorf("[下载] 发送 %s 的 HTTP 请求失败：%v", targetURL, err)
		return nil, fmt.Errorf("发送 %s 的 HTTP 请求失败：%v", targetUrl, err)
	}

	// 手动处理 302 重定向
	if resp.StatusCode == http.StatusFound {
		// 获取 Location 头
		location := resp.Header.Get("Location")
		if location != "" {
			AppLogger.Infof("[下载] 手动处理 302 重定向：%s -> %s", targetUrl, location)
			resp.Body.Close()

			// 为新请求创建传输对象
			redirectTransport := &http.Transport{}
			// // 设置代理（与原始请求相同）
			// proxyURL := "http://127.0.0.1:10808"
			// proxy, perr := url.Parse(proxyURL)
			// if perr != nil {
			// 	AppLogger.Warnf("[下载] 解析代理 URL 失败：%v", perr)
			// } else {
			// 	redirectTransport.Proxy = http.ProxyURL(proxy)
			// 	AppLogger.Infof("[下载] 使用代理：%s", proxyURL)
			// }

			// 创建新请求
			redirectReq, err := http.NewRequest("GET", location, nil)
			if err != nil {
				AppLogger.Errorf("[下载] 创建重定向请求失败：%v", err)
				return nil, fmt.Errorf("创建重定向请求失败：%v", err)
			}
			redirectReq.Header.Set("User-Agent", userAgent)

			// 设置与原始请求相同的头信息
			// for k, v := range req.Header {
			// 	redirectReq.Header[k] = v
			// }

			// 设置 Referer 头
			// redirectReq.Header.Set("Referer", targetURL)

			// 发送重定向请求
			redirectClient := &http.Client{
				Transport: redirectTransport,
				Timeout:   60 * time.Second,
			}
			resp, err = redirectClient.Do(redirectReq)
			if err != nil {
				AppLogger.Errorf("[下载] 发送重定向请求失败：%v", err)
				return nil, fmt.Errorf("发送重定向请求失败：%v", err)
			}
			// 检查重定向后的状态码
			if resp.StatusCode != http.StatusOK {
				resp.Body.Close()
				AppLogger.Errorf("[下载] 重定向后请求失败，HTTP 状态码：%d", resp.StatusCode)
				return nil, fmt.Errorf("重定向后请求失败，HTTP 状态码：%d", resp.StatusCode)
			}
		} else {
			resp.Body.Close()
			AppLogger.Errorf("[下载] 302 重定向但没有 Location 头")
			return nil, fmt.Errorf("302 重定向但没有 Location 头")
		}
	} else if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		AppLogger.Errorf("[下载] 请求失败，HTTP 状态码：%d", resp.StatusCode)
		return nil, fmt.Errorf("请求失败，HTTP 状态码：%d", resp.StatusCode)
	}
	defer resp.Body.Close()

	// 读取响应内容
	content, err = io.ReadAll(resp.Body)
	if err != nil {
		AppLogger.Errorf("[下载] 读取 %s 的 HTTP 响应失败：%v", targetUrl, err)
		return nil, fmt.Errorf("读取 %s 的 HTTP 响应失败：%v", targetUrl, err)
	}
	return content, nil
}

func DownloadFile(targetUrl string, filePath string, userAgent string) (err error) {
	// 创建请求并设置 User-Agent
	req, err := http.NewRequest("GET", targetUrl, nil)
	if err != nil {
		AppLogger.Errorf("[下载] 创建 %s 的 HTTP 请求失败：%v", targetUrl, err)
		return fmt.Errorf("创建 %s 的 HTTP 请求失败：%v", targetUrl, err)
	}
	req.Header.Set("User-Agent", userAgent)

	// 创建传输对象并配置代理
	transport := &http.Transport{}

	// // 设置代理
	// proxyURL := "http://127.0.0.1:10808"
	// proxy, perr := url.Parse(proxyURL)
	// if perr != nil {
	// 	AppLogger.Warnf("[下载] 解析代理 URL 失败：%v", perr)
	// } else {
	// 	transport.Proxy = http.ProxyURL(proxy)
	// 	AppLogger.Infof("[下载] 使用代理：%s", proxyURL)
	// }

	// 发送请求 - 配置客户端支持重定向
	client := &http.Client{
		Transport: transport,
		Timeout:   300 * time.Second,
		// 自定义重定向策略，确保正确传递请求头
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		AppLogger.Errorf("[下载] 发送 %s 的 HTTP 请求失败：%v", targetUrl, err)
		return fmt.Errorf("发送 %s 的 HTTP 请求失败：%v", targetUrl, err)
	}

	// 手动处理 302 重定向
	if resp.StatusCode == http.StatusFound {
		// 获取 Location 头
		location := resp.Header.Get("Location")
		if location != "" {
			AppLogger.Infof("[下载] 手动处理 302 重定向：%s -> %s", targetUrl, location)
			resp.Body.Close()

			// 为新请求创建传输对象
			redirectTransport := &http.Transport{}
			// proxy, perr := url.Parse(proxyURL)
			// if perr != nil {
			// 	AppLogger.Warnf("[下载] 解析代理 URL 失败：%v", perr)
			// } else {
			// 	redirectTransport.Proxy = http.ProxyURL(proxy)
			// }

			// 创建新请求
			redirectReq, err := http.NewRequest("GET", location, nil)
			if err != nil {
				AppLogger.Errorf("[下载] 创建重定向请求失败：%v", err)
				return fmt.Errorf("创建重定向请求失败：%v", err)
			}
			redirectReq.Header.Set("User-Agent", userAgent)
			// 发送重定向请求
			redirectClient := &http.Client{
				Transport: redirectTransport,
				Timeout:   60 * time.Second,
			}
			resp, err = redirectClient.Do(redirectReq)
			if err != nil {
				AppLogger.Errorf("[下载] 发送重定向请求失败：%v", err)
				return fmt.Errorf("发送重定向请求失败：%v", err)
			}
			// 检查重定向后的状态码
			if resp.StatusCode != http.StatusOK {
				AppLogger.Errorf("[下载] 重定向后下载失败，HTTP 状态码：%d", resp.StatusCode)
				return fmt.Errorf("重定向后下载失败，HTTP 状态码：%d", resp.StatusCode)
			}
		} else {
			resp.Body.Close()
			AppLogger.Errorf("[下载] 302 重定向但没有 Location 头")
			return fmt.Errorf("302 重定向但没有 Location 头")
		}
	} else if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		AppLogger.Errorf("[下载] 下载 %s 失败，HTTP 状态码：%d", targetUrl, resp.StatusCode)
		return fmt.Errorf("下载 %s 失败，HTTP 状态码：%d", targetUrl, resp.StatusCode)
	}
	defer resp.Body.Close()

	// 读取响应内容
	content, err := io.ReadAll(resp.Body)
	if err != nil {
		AppLogger.Errorf("[下载] 读取 %s 的 HTTP 响应失败：%v", targetUrl, err)
		return fmt.Errorf("读取 %s 的 HTTP 响应失败：%v", targetUrl, err)
	}
	// folder := filepath.Dir(filePath)
	// os.MkdirAll(folder, 0777)
	err = WriteFileWithPerm(filePath, content, 0777)
	if err != nil {
		AppLogger.Errorf("[下载] 写入 %s 失败：%v", filePath, err)
		return fmt.Errorf("写入 %s 失败：%v", filePath, err)
	}
	// 检查目标文件是否存在
	os.Chmod(filepath.Dir(filePath), 0777)
	os.Chmod(filePath, 0777)
	AppLogger.Infof("[下载] %s => %s 成功", targetUrl, filePath)
	return nil
}

// 给一个 URL 发 POST 请求，不处理返回值
func PostUrl(targetUrl string) error {
	// 创建请求并设置 User-Agent
	req, err := http.NewRequest("POST", targetUrl, nil)
	if err != nil {
		AppLogger.Errorf("创建 %s 的 HTTP POST 请求失败：%v", targetUrl, err)
		return fmt.Errorf("创建 %s 的 HTTP POST 请求失败：%v", targetUrl, err)
	}
	// 创建传输对象并配置代理
	transport := &http.Transport{}
	// 发送请求 - 禁用自动重定向，改为手动处理
	client := &http.Client{
		Transport: transport,
		Timeout:   60 * time.Second,
		// 禁用自动重定向，返回最后一个响应
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	_, err = client.Do(req)
	if err != nil {
		return fmt.Errorf("发送 %s 的 HTTP POST 请求失败：%v", targetUrl, err)
	}
	return nil
}

// DownloadProgressCallback 下载进度回调函数类型
type DownloadProgressCallback func(bytesRead int64, totalBytes int64)

// DownloadFileWithProgress 带进度的文件下载方法
// ctx - 上下文，可用于取消下载
func DownloadFileWithProgress(ctx context.Context, proxyUrl, downloadUrl string, fileName string, userAgent string, callback DownloadProgressCallback) (err error) {
	// 创建请求并设置 User-Agent 和上下文
	req, err := http.NewRequestWithContext(ctx, "GET", downloadUrl, nil)
	if err != nil {
		AppLogger.Errorf("[下载] 创建 %s 的 HTTP 请求失败：%v", downloadUrl, err)
		return fmt.Errorf("创建 %s 的 HTTP 请求失败：%v", downloadUrl, err)
	}
	req.Header.Set("User-Agent", userAgent)

	// 创建传输对象
	transport := &http.Transport{}

	// 如果提供了代理 URL，则配置代理
	if proxyUrl != "" {
		proxy, perr := url.Parse(proxyUrl)
		if perr != nil {
			AppLogger.Warnf("[下载] 解析代理 URL 失败：%v，将不使用代理", validation.ProxyParseError(perr))
		} else {
			transport.Proxy = http.ProxyURL(proxy)
			// url.URL.Redacted() 只遮蔽密码，用户名仍是明文，这里统一用代理专用脱敏
			AppLogger.Infof("[下载] 使用代理：%s", validation.RedactParsedProxyURL(proxy))
		}
	}

	// 发送请求 - 注意：使用 context 控制超时，这里不设置客户端超时
	client := &http.Client{
		Transport: transport,
		// 不设置 Timeout，使用 context 控制超时和取消
	}
	resp, err := client.Do(req)
	if err != nil {
		AppLogger.Errorf("[下载] 发送 %s 的 HTTP 请求失败：%v", downloadUrl, err)
		return fmt.Errorf("发送 %s 的 HTTP 请求失败：%v", downloadUrl, err)
	}
	// 检查 HTTP 响应状态码
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		AppLogger.Errorf("[下载] 下载 %s 失败，HTTP 状态码：%d", downloadUrl, resp.StatusCode)
		return fmt.Errorf("下载 %s 失败，HTTP 状态码：%d", downloadUrl, resp.StatusCode)
	}
	defer resp.Body.Close()

	// 确保目录存在
	err = os.MkdirAll(filepath.Dir(fileName), 0777)
	if err != nil {
		AppLogger.Errorf("[下载] 创建目录失败：%v", err)
		return fmt.Errorf("创建目录失败：%v", err)
	}

	// 创建文件
	file, err := os.Create(fileName)
	if err != nil {
		AppLogger.Errorf("[下载] 创建文件 %s 失败：%v", fileName, err)
		return fmt.Errorf("创建文件 %s 失败：%v", fileName, err)
	}
	defer file.Close()

	// 获取文件总大小
	totalBytes := resp.ContentLength
	var bytesRead int64 = 0

	// 分块下载
	buffer := make([]byte, 32*1024) // 32 KB 缓冲
	lastProgressUpdate := time.Now()

	for {
		// 检查上下文是否被取消
		select {
		case <-ctx.Done():
			AppLogger.Infof("[下载] %s 下载被取消", downloadUrl)
			return ctx.Err()
		default:
			// 继续下载
		}

		// 读取数据块
		n, rerr := resp.Body.Read(buffer)
		if rerr != nil && rerr != io.EOF {
			AppLogger.Errorf("[下载] 读取 %s 的数据失败：%v", downloadUrl, rerr)
			return fmt.Errorf("读取 %s 的数据失败：%v", downloadUrl, rerr)
		}

		if n == 0 {
			break // 下载完成
		}

		// 写入文件
		_, err = file.Write(buffer[:n])
		if err != nil {
			AppLogger.Errorf("[下载] 写入 %s 的数据失败：%v", fileName, err)
			return fmt.Errorf("写入 %s 的数据失败：%v", fileName, err)
		}

		// 更新已读取字节数
		bytesRead += int64(n)

		// 每隔一定时间或每读取一定量数据就回调一次进度
		now := time.Now()
		if callback != nil && (now.Sub(lastProgressUpdate) > 500*time.Millisecond || bytesRead == totalBytes) {
			callback(bytesRead, totalBytes)
			lastProgressUpdate = now
		}
	}

	// 确保文件权限
	err = os.Chmod(fileName, 0777)
	if err != nil {
		AppLogger.Warnf("[下载] 设置文件权限失败：%v", err)
		// 不返回错误，因为下载本身成功了
	}

	// 检查目标文件是否存在
	if !PathExists(fileName) {
		AppLogger.Errorf("[下载] 写入完成，但是文件不存在：%s", fileName)
		return fmt.Errorf("写入完成，但是文件不存在：%s", fileName)
	}

	AppLogger.Infof("[下载] %s => %s 成功，文件大小：%d 字节", downloadUrl, fileName, bytesRead)
	return nil
}

// TestURLConnection 测试 URL 是否可以连接
// proxyURL - 代理 URL，如果为空则不使用代理
// testURL - 要测试的 URL
// timeout - 连接超时时间（秒）
func TestURLConnection(proxyUrl, testUrl string, timeout int) (bool, error) {
	// 验证 URL 格式
	_, err := url.Parse(testUrl)
	if err != nil {
		AppLogger.Errorf("[网络测试] 无效的 URL 格式：%s，错误：%v", testUrl, err)
		return false, fmt.Errorf("无效的 URL 格式：%v", err)
	}

	// 创建传输对象
	transport := &http.Transport{}

	// 如果提供了代理 URL，则配置代理
	if proxyUrl != "" {
		proxy, perr := url.Parse(proxyUrl)
		if perr != nil {
			AppLogger.Warnf("[网络测试] 解析代理 URL 失败：%v，将不使用代理", validation.ProxyParseError(perr))
		} else {
			transport.Proxy = http.ProxyURL(proxy)
			// url.URL.Redacted() 只遮蔽密码，用户名仍是明文，这里统一用代理专用脱敏
			AppLogger.Infof("[网络测试] 使用代理：%s", validation.RedactParsedProxyURL(proxy))
		}
	}

	// 创建 HTTP 客户端
	client := &http.Client{
		Transport: transport,
		Timeout:   time.Duration(timeout) * time.Second,
	}

	AppLogger.Infof("[网络测试] 开始测试连接：%s，超时设置：%d 秒", testUrl, timeout)

	// 发送 HEAD 请求以测试连接性（HEAD 请求只获取头信息，不下载内容）
	startTime := time.Now()
	req, err := http.NewRequest("HEAD", testUrl, nil)
	if err != nil {
		AppLogger.Errorf("[网络测试] 创建请求失败：%v", err)
		return false, fmt.Errorf("创建请求失败：%v", err)
	}

	// 设置合理的 User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")

	// 发送请求
	resp, err := client.Do(req)
	elapsed := time.Since(startTime)

	if err != nil {
		AppLogger.Errorf("[网络测试] 连接失败，准备启用网络代理：%s，错误：%v，耗时：%v", testUrl, err, elapsed)
		return false, fmt.Errorf("连接失败：%v", err)
	}
	defer resp.Body.Close()

	// 检查 HTTP 状态码
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		AppLogger.Warnf("[网络测试] 连接成功但状态码异常：%s，状态码：%d，耗时：%v", testUrl, resp.StatusCode, elapsed)
		return false, fmt.Errorf("连接成功但状态码异常：%d", resp.StatusCode)
	}

	AppLogger.Infof("[网络测试] 连接成功：%s，状态码：%d，耗时：%v", testUrl, resp.StatusCode, elapsed)
	return true, nil
}

// TestURLConnectionWithDefaultTimeout 使用默认超时时间（5 秒）测试 URL 连接
func TestURLConnectionWithDefaultTimeout(proxyUrl, testUrl string) (bool, error) {
	return TestURLConnection(proxyUrl, testUrl, 5)
}

// 便捷函数：不使用 context 的下载方法，用于向后兼容
func DownloadFileWithProgressWithoutContext(proxyUrl, downloadUrl string, fileName string, userAgent string, callback DownloadProgressCallback) error {
	return DownloadFileWithProgress(context.Background(), proxyUrl, downloadUrl, fileName, userAgent, callback)
}

// TestGithub 测试 GitHub 连接，使用智能管理器选择最佳连接方式
// url - 要访问的 GitHub URL
// proxy - 代理配置（已废弃，保留是为了向后兼容）
// 返回值：
//   - 如果连接成功，返回实际使用的 URL（可能是原始 URL 或代理 URL）
//   - 如果连接失败，返回"failed"
func TestGithub(url string, proxy string) (github.ConnectionType, string) {
	manager := github.GetManager()

	// 获取最佳连接方式
	access, err := manager.GetBestConnection()
	if err != nil {
		AppLogger.Warnf("[GitHub] 无法获取最佳连接：%v", err)
		return github.ConnectionTypeFailed, "failed"
	}

	// 根据连接类型返回对应的 URL
	switch access.Type {
	case github.ConnectionTypeDirect:
		// 直连，直接返回原 URL
		return access.Type, url
	case github.ConnectionTypeProxy:
		// 用户代理，返回代理 URL
		return access.Type, url
	case github.ConnectionTypeGitHubProxy:
		// GitHub 代理 URL，使用硬编码的代理前缀
		proxyUrl := fmt.Sprintf("%s%s", "https://gh.llkk.cc/", url)
		return access.Type, proxyUrl
	default:
		AppLogger.Warnf("[GitHub] 未知的连接类型：%s", access.Type)
		return access.Type, "failed"
	}
}
