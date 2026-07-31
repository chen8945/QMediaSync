package github

import (
	"bytes"
	"log"
	"net/url"
	"strings"
	"testing"
	"time"

	"qmediasync/internal/validation"
)

// useTestLogPrintf 把本包日志重定向到 buffer，便于断言日志内容。
func useTestLogPrintf(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	SetLogPrintf(logger.Printf)
	t.Cleanup(func() {
		SetLogPrintf(nil)
	})
	return &buf
}

// 遮蔽规则本身由 validation 包的测试覆盖，这里只钉住本包出口不泄露凭据。

// TestGetBestConnectionErrorRedactsProxy 代理不可用时返回的错误会被 helpers.TestGithub 以 WARN 写日志。
func TestGetBestConnectionErrorRedactsProxy(t *testing.T) {
	manager := &Manager{
		testTimeout: time.Millisecond,
		cacheValid:  10 * time.Minute,
		// 端口 1 上不会有代理监听，TestConnection 必然失败，从而走到返回错误的分支
		httpProxy: "socks5://user:secret@127.0.0.1:1",
	}

	_, err := manager.GetBestConnection()
	if err == nil {
		t.Fatal("代理不可用时应返回错误")
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("错误文案泄露了密码：%q", err.Error())
	}
	if !strings.Contains(err.Error(), "xxxxx") {
		t.Fatalf("错误文案应包含脱敏占位符，实际：%q", err.Error())
	}
}

// TestGitHubAccessProxyURLRedacted GitHubAccess.ProxyURL 会被展示和写日志，只能存脱敏地址；
// 真正拨号靠 Client 内的 Transport，因此遮蔽不影响连通性。
func TestGitHubAccessProxyURLRedacted(t *testing.T) {
	proxy, err := url.Parse("socks5://user:secret@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("解析代理地址失败：%v", err)
	}
	access := &GitHubAccess{ProxyURL: validation.RedactParsedProxyURL(proxy)}
	if strings.Contains(access.ProxyURL, "secret") {
		t.Fatalf("ProxyURL 泄露了密码：%q", access.ProxyURL)
	}
	if access.ProxyURL != "socks5://xxxxx:xxxxx@127.0.0.1:1080" {
		t.Fatalf("ProxyURL = %q，期望脱敏结果", access.ProxyURL)
	}
}

// TestTestConnectionParseFailureRedactsLog 解析失败的日志不得回显原始地址。
func TestTestConnectionParseFailureRedactsLog(t *testing.T) {
	buf := useTestLogPrintf(t)
	manager := &Manager{testTimeout: time.Millisecond}

	if manager.TestConnection(ConnectionTypeProxy, "http://user:secret@[::1:1080") {
		t.Fatal("无效代理地址应返回 false")
	}
	if strings.Contains(buf.String(), "secret") {
		t.Fatalf("解析失败日志泄露了密码：%s", buf.String())
	}
}
