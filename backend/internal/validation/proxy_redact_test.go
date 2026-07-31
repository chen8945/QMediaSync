package validation

import (
	"net/url"
	"strings"
	"testing"
)

// TestRedactProxyURL 锁定凭据遮蔽行为。标准库 Redacted() 在用户名和 opaque 地址上都会漏，
// 这里逐条钉住，避免后续调用点被改回 Redacted()。
func TestRedactProxyURL(t *testing.T) {
	cases := []struct {
		name     string
		raw      string
		want     string
		leakFree []string
	}{
		{
			name:     "用户名和密码都遮蔽",
			raw:      "socks5://user:secret@127.0.0.1:1080",
			want:     "socks5://xxxxx:xxxxx@127.0.0.1:1080",
			leakFree: []string{"user", "secret"},
		},
		{
			name:     "只有用户名时同样遮蔽",
			raw:      "socks5://user@127.0.0.1:1080",
			want:     "socks5://xxxxx@127.0.0.1:1080",
			leakFree: []string{"user"},
		},
		{
			name:     "企业代理的域账号不能外泄",
			raw:      "http://DOMAIN%5Cjsmith:pw@proxy.corp:8080",
			want:     "http://xxxxx:xxxxx@proxy.corp:8080",
			leakFree: []string{"jsmith", "DOMAIN", "pw@"},
		},
		{
			name:     "opaque 地址保留主机但遮蔽凭据",
			raw:      "socks5:user:secret@h:1080",
			want:     "socks5:xxxxx@h:1080",
			leakFree: []string{"user", "secret"},
		},
		{
			name: "无凭据地址原样返回",
			raw:  "http://127.0.0.1:7890",
			want: "http://127.0.0.1:7890",
		},
		{
			name: "省略主机名的本地代理简写保持可读",
			raw:  "http://:1080",
			want: "http://:1080",
		},
		{
			name: "空值返回空串",
			raw:  "",
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := RedactProxyURL(tc.raw)
			if got != tc.want {
				t.Fatalf("RedactProxyURL(%q) = %q，期望 %q", tc.raw, got, tc.want)
			}
			for _, leak := range tc.leakFree {
				if strings.Contains(got, leak) {
					t.Fatalf("遮蔽结果 %q 仍包含凭据片段 %q", got, leak)
				}
			}
		})
	}
}

// TestRedactProxyURLUnparsable 解析失败时不得回显原串，否则凭据照样进日志。
func TestRedactProxyURLUnparsable(t *testing.T) {
	for _, raw := range []string{
		"socks5://user:secret@127.0.0.1:10\t80", // 控制字符
		"http://user:secret@[::1:1080",          // IPv6 方括号不闭合
	} {
		got := RedactProxyURL(raw)
		if got != proxyUnparsablePlaceholder {
			t.Fatalf("RedactProxyURL(%q) = %q，期望占位文案 %q", raw, got, proxyUnparsablePlaceholder)
		}
		if strings.Contains(got, "secret") || strings.Contains(got, "user") {
			t.Fatalf("占位文案 %q 不应包含原始凭据", got)
		}
	}
}

// TestRedactParsedProxyURLDoesNotMutate 调用方常在遮蔽后继续用同一个 URL 拨号，入参被改会导致代理失效。
func TestRedactParsedProxyURLDoesNotMutate(t *testing.T) {
	parsed, err := url.Parse("socks5://user:secret@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("准备用例失败：%v", err)
	}

	if got := RedactParsedProxyURL(parsed); got != "socks5://xxxxx:xxxxx@127.0.0.1:1080" {
		t.Fatalf("遮蔽结果异常：%q", got)
	}
	if parsed.String() != "socks5://user:secret@127.0.0.1:1080" {
		t.Fatalf("入参被修改，实际为 %q", parsed.String())
	}
}

// TestProxyParseErrorDropsRawURL url.Error.Error() 会渲染成 parse "<整个原串>"，必须剥掉。
func TestProxyParseErrorDropsRawURL(t *testing.T) {
	_, err := url.Parse("http://user:secret@[::1:1080")
	if err == nil {
		t.Fatal("构造解析错误失败")
	}
	got := ProxyParseError(err).Error()
	if strings.Contains(got, "secret") {
		t.Fatalf("错误文案泄露了密码：%q", got)
	}
}
