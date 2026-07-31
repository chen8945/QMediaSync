package controllers

import (
	"strings"
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/requests"
	"qmediasync/internal/validation"
)

// setupProxyMaskingTest 设置被测的存储代理地址，并在结束后还原全局状态。
func setupProxyMaskingTest(t *testing.T, stored string) {
	t.Helper()

	oldSettings := models.SettingsGlobal
	t.Cleanup(func() {
		models.SettingsGlobal = oldSettings
	})
	models.SettingsGlobal = &models.Settings{HttpProxy: stored}
}

// TestResolveSubmittedHTTPProxyRestoresMaskedValue 回传的脱敏串原样提交时必须还原为存储的真实凭据，
// 否则字面量 xxxxx 会被存成真实密码，代理直接失效。
func TestResolveSubmittedHTTPProxyRestoresMaskedValue(t *testing.T) {
	const stored = "socks5://user:secret@10.0.0.5:1080"
	setupProxyMaskingTest(t, stored)

	// 模拟 GetHttpProxy 回传给前端、再被原样提交回来的值
	masked := "socks5://xxxxx:xxxxx@10.0.0.5:1080"
	if got := resolveSubmittedHTTPProxy(masked, true); got != stored {
		t.Fatalf("提交脱敏串应还原为存储原值，实际为 %q", got)
	}
}

// TestResolveSubmittedHTTPProxyKeepsRealEdits 用户真的改了地址时不能被误判为未修改。
func TestResolveSubmittedHTTPProxyKeepsRealEdits(t *testing.T) {
	setupProxyMaskingTest(t, "socks5://user:secret@10.0.0.5:1080")

	cases := []struct {
		name      string
		submitted string
	}{
		{"换成无凭据地址", "http://127.0.0.1:7890"},
		{"换成另一组凭据", "socks5://alice:pw@10.0.0.5:1080"},
		{"只换主机", "socks5://user:secret@10.0.0.9:1080"},
		{"清空代理", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveSubmittedHTTPProxy(tc.submitted, false); got != tc.submitted {
				t.Fatalf("提交 %q 应原样保留，实际为 %q", tc.submitted, got)
			}
		})
	}
}

// TestResolveSubmittedHTTPProxyAcceptsExplicitMaskCredential 锁定审查发现的场景：
// 用户主动把凭据改成 xxxxx 时，显式 false 必须让它作为真实凭据保存。
func TestResolveSubmittedHTTPProxyAcceptsExplicitMaskCredential(t *testing.T) {
	setupProxyMaskingTest(t, "socks5://old:secret@10.0.0.5:1080")

	const submitted = "socks5://xxxxx:xxxxx@10.0.0.5:1080"
	if got := resolveSubmittedHTTPProxy(submitted, false); got != submitted {
		t.Fatalf("显式替换为 xxxxx 时应保留新凭据，实际为 %q", got)
	}
}

// TestResolveSubmittedHTTPProxyDoesNotPreserveCredentialsForChangedEndpoint
// 禁止带 preserve_proxy_credentials 的请求将已存代理凭据发送给其他代理端点。
func TestResolveSubmittedHTTPProxyDoesNotPreserveCredentialsForChangedEndpoint(t *testing.T) {
	setupProxyMaskingTest(t, "socks5://user:secret@10.0.0.5:1080")

	cases := []struct {
		name      string
		submitted string
	}{
		{"变更协议", "http://xxxxx:xxxxx@10.0.0.5:1080"},
		{"变更主机", "socks5://xxxxx:xxxxx@10.0.0.9:1080"},
		{"变更端口", "socks5://xxxxx:xxxxx@10.0.0.5:1081"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSubmittedHTTPProxy(tc.submitted, true)
			if got != tc.submitted {
				t.Fatalf("变更端点后代理地址 = %q，期望 %q", got, tc.submitted)
			}
			if strings.Contains(got, "user:secret") {
				t.Fatalf("变更端点后仍保留了存储凭据：%q", got)
			}
		})
	}
}

// TestResolveSubmittedHTTPProxyWithoutCredentials 无凭据地址的脱敏结果与原值相同，
// 走还原逻辑也必须得到同一个值，不能因为相等就被特殊处理。
func TestResolveSubmittedHTTPProxyWithoutCredentials(t *testing.T) {
	const stored = "http://127.0.0.1:7890"
	setupProxyMaskingTest(t, stored)

	if got := resolveSubmittedHTTPProxy(stored, true); got != stored {
		t.Fatalf("无凭据地址应原样返回，实际为 %q", got)
	}
}

// TestResolveSubmittedHTTPProxyFromEmptyStored 此前没配过代理时，提交值一律照用，
// 不能因为存储值为空就把新地址吞掉。
func TestResolveSubmittedHTTPProxyFromEmptyStored(t *testing.T) {
	setupProxyMaskingTest(t, "")

	const submitted = "socks5://user:secret@10.0.0.5:1080"
	if got := resolveSubmittedHTTPProxy(submitted, false); got != submitted {
		t.Fatalf("存储值为空时应原样返回提交值，实际为 %q", got)
	}
}

func TestResolveSubmittedHTTPProxyAllowsClearWithPreserveFlag(t *testing.T) {
	setupProxyMaskingTest(t, "socks5://user:secret@10.0.0.5:1080")

	if got := resolveSubmittedHTTPProxy("", true); got != "" {
		t.Fatalf("清空代理时应优先保留空值，实际为 %q", got)
	}
}

// TestGetHttpProxyResponseHidesCredentials 锁定接口回传不含明文凭据。
// 这是审查发现的原始缺陷：任意 JWT 或 API Key 持有者都能读到代理密码。
func TestGetHttpProxyResponseHidesCredentials(t *testing.T) {
	const stored = "socks5://user:secret@10.0.0.5:1080"
	setupProxyMaskingTest(t, stored)

	// 直接验证回传值的构造逻辑，避免依赖 gin 路由装配；GetHttpProxy 用的就是这个函数
	redacted := validation.RedactProxyURL(stored)
	if strings.Contains(redacted, "secret") {
		t.Fatalf("回传值 %q 仍包含密码", redacted)
	}
	if strings.Contains(redacted, "user") {
		t.Fatalf("回传值 %q 仍包含用户名", redacted)
	}
	if !strings.Contains(redacted, "10.0.0.5:1080") {
		t.Fatalf("回传值 %q 应保留主机和端口便于用户辨认", redacted)
	}
	// 回传值必须能被还原回真实地址，否则前端保存就会写坏配置
	if got := resolveSubmittedHTTPProxy(redacted, true); got != stored {
		t.Fatalf("回传值无法还原：%q", got)
	}
}

func TestShouldPreserveProxyCredentials(t *testing.T) {
	const stored = "socks5://user:secret@10.0.0.5:1080"
	setupProxyMaskingTest(t, stored)
	masked := validation.RedactProxyURL(stored)
	trueValue := true
	falseValue := false

	cases := []struct {
		name string
		req  requests.HTTPProxyRequest
		want bool
	}{
		{
			name: "新前端未编辑时显式保留",
			req: requests.HTTPProxyRequest{
				HTTPProxy:                masked,
				PreserveProxyCredentials: &trueValue,
			},
			want: true,
		},
		{
			name: "新前端编辑后显式替换",
			req: requests.HTTPProxyRequest{
				HTTPProxy:                masked,
				PreserveProxyCredentials: &falseValue,
			},
			want: false,
		},
		{
			name: "旧前端回传脱敏值兼容保留",
			req:  requests.HTTPProxyRequest{HTTPProxy: masked},
			want: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldPreserveProxyCredentials(tc.req); got != tc.want {
				t.Fatalf("shouldPreserveProxyCredentials() = %v，期望 %v", got, tc.want)
			}
		})
	}
}
