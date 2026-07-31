package requests

import (
	"encoding/json"
	"net/url"
	"testing"
)

func TestConnectionRequestValidate(t *testing.T) {
	t.Run("保存 HTTP 代理允许空", func(t *testing.T) {
		req := HTTPProxyRequest{}
		if err := req.ValidateSave(); err != nil {
			t.Fatalf("ValidateSave() error = %v", err)
		}
	})

	t.Run("测试 HTTP 代理必须非空", func(t *testing.T) {
		req := HTTPProxyRequest{}
		if err := req.ValidateTest(); err == nil {
			t.Fatal("ValidateTest() error = nil, want error")
		}
	})

	t.Run("代理 detailed 枚举错误失败", func(t *testing.T) {
		req := HTTPProxyRequest{HTTPProxy: "http://127.0.0.1:7890", Detailed: 2}
		if err := req.ValidateTest(); err == nil {
			t.Fatal("ValidateTest() error = nil, want error")
		}
	})

	t.Run("账号 ID 通过", func(t *testing.T) {
		req := AccountIDRequest{AccountID: 1}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

// TestNormalizedHTTPProxy 校验层对副本做 TrimSpace，保存和拨号必须用同一份规范化结果，
// 否则带首尾空白的地址会通过校验但解析失败。控制器改回直接用 req.HTTPProxy 时本测试必须失败。
func TestNormalizedHTTPProxy(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{name: "去掉首尾空格", raw: "  http://127.0.0.1:7890  ", want: "http://127.0.0.1:7890"},
		{name: "去掉制表符和换行", raw: "\t socks5://127.0.0.1:1080 \n", want: "socks5://127.0.0.1:1080"},
		{name: "纯空白规范化为空", raw: "   ", want: ""},
		{name: "空串保持为空", raw: "", want: ""},
		{name: "无空白保持原样", raw: "socks5h://user:pass@10.0.0.5:1080", want: "socks5h://user:pass@10.0.0.5:1080"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := HTTPProxyRequest{HTTPProxy: tc.raw}
			if got := req.NormalizedHTTPProxy(); got != tc.want {
				t.Fatalf("NormalizedHTTPProxy() = %q，期望 %q", got, tc.want)
			}
		})
	}
}

// TestNormalizedHTTPProxy与校验结果一致 带首尾空白的地址应能通过校验，且规范化结果可直接解析。
func TestNormalizedHTTPProxy与校验结果一致(t *testing.T) {
	req := HTTPProxyRequest{HTTPProxy: "  socks5://127.0.0.1:1080  "}
	if err := req.ValidateSave(); err != nil {
		t.Fatalf("ValidateSave() error = %v", err)
	}
	parsed, err := url.Parse(req.NormalizedHTTPProxy())
	if err != nil {
		t.Fatalf("规范化后的地址应可解析，实际 error = %v", err)
	}
	if parsed.Host != "127.0.0.1:1080" {
		t.Fatalf("解析出的 Host = %q，期望 127.0.0.1:1080", parsed.Host)
	}
	// 未规范化的原串会把空白带进 Host，出站必然失败
	if raw, rawErr := url.Parse(req.HTTPProxy); rawErr == nil && raw.Host == "127.0.0.1:1080" {
		t.Fatal("原串不应与规范化结果等价，否则本测试无法拦截回退")
	}
}

func TestHTTPProxyRequestPreserveProxyCredentialsBinding(t *testing.T) {
	cases := []struct {
		name string
		body string
		want *bool
	}{
		{name: "显式保留", body: `{"http_proxy":"socks5://xxxxx:xxxxx@127.0.0.1:1080","preserve_proxy_credentials":true}`, want: boolPtr(true)},
		{name: "显式替换", body: `{"http_proxy":"socks5://xxxxx:xxxxx@127.0.0.1:1080","preserve_proxy_credentials":false}`, want: boolPtr(false)},
		{name: "兼容旧客户端缺省字段", body: `{"http_proxy":"socks5://xxxxx:xxxxx@127.0.0.1:1080"}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var req HTTPProxyRequest
			if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			if tc.want == nil {
				if req.PreserveProxyCredentials != nil {
					t.Fatalf("PreserveProxyCredentials = %v，期望 nil", *req.PreserveProxyCredentials)
				}
				return
			}
			if req.PreserveProxyCredentials == nil || *req.PreserveProxyCredentials != *tc.want {
				t.Fatalf("PreserveProxyCredentials = %v，期望 %v", req.PreserveProxyCredentials, *tc.want)
			}
		})
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func TestOAuthRequestValidate(t *testing.T) {
	t.Run("OAuth URL 请求通过", func(t *testing.T) {
		req := OAuthURLRequest{AccountID: 1, RedirectURL: "https://example.com/callback"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("OAuth URL redirect 非法失败", func(t *testing.T) {
		req := OAuthURLRequest{AccountID: 1, RedirectURL: "ftp://example.com"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("OAuth Confirm 缺少 data 和 payload 失败", func(t *testing.T) {
		req := OAuthConfirmRequest{AccountID: 1}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("OAuth Status 通过", func(t *testing.T) {
		req := OAuthStatusRequest{AccountID: 1, State: "state"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}

func TestRemoteFileURLRequestValidate(t *testing.T) {
	t.Run("PickCode 查询通过", func(t *testing.T) {
		req := RemoteFileURLRequest{PickCode: "abc", Force: 1}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("PickCode 为空失败", func(t *testing.T) {
		req := RemoteFileURLRequest{}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("Force 枚举错误失败", func(t *testing.T) {
		req := RemoteFileURLRequest{PickCode: "abc", Force: 2}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestProxy115RequestValidate(t *testing.T) {
	t.Run("115 CDN URL 通过", func(t *testing.T) {
		req := Proxy115Request{URL: "https://cdn.115cdn.net/file.mp4"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("未知域名失败", func(t *testing.T) {
		req := Proxy115Request{URL: "https://example.com/file.mp4"}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})
}

func TestConnectionAuxRequestValidate(t *testing.T) {
	t.Run("队列限流通过", func(t *testing.T) {
		req := QueueRateLimitRequest{QPS: 1, QPM: 60, QPH: 3600}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})

	t.Run("清理统计天数超限失败", func(t *testing.T) {
		req := CleanRequestStatsRequest{Days: 366}
		if err := req.Validate(); err == nil {
			t.Fatal("Validate() error = nil, want error")
		}
	})

	t.Run("OpenList 直链请求通过", func(t *testing.T) {
		req := OpenListFileURLRequest{AccountID: 1, Path: "/movie.mp4"}
		if err := req.Validate(); err != nil {
			t.Fatalf("Validate() error = %v", err)
		}
	})
}
