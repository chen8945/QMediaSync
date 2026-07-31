package validation

import (
	"strings"
	"testing"
)

func TestRangeInt(t *testing.T) {
	tests := []struct {
		name    string
		value   int
		min     int
		max     int
		wantErr bool
	}{
		{name: "范围内通过", value: 5, min: 1, max: 10},
		{name: "小于最小值失败", value: 0, min: 1, max: 10, wantErr: true},
		{name: "大于最大值失败", value: 11, min: 1, max: 10, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := RangeInt("download_threads", tt.value, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("RangeInt() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestOneOfInt(t *testing.T) {
	if err := OneOfInt("download_meta", 1, []int{0, 1}); err != nil {
		t.Fatalf("OneOfInt() error = %v", err)
	}
	if err := OneOfInt("download_meta", 2, []int{0, 1}); err == nil {
		t.Fatal("OneOfInt() expected error")
	}
}

func TestHTTPURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		allowEmpty bool
		wantErr    bool
	}{
		{name: "HTTP 通过", raw: "http://127.0.0.1:8096"},
		{name: "HTTPS 通过", raw: "https://emby.example.com"},
		{name: "空值允许", raw: "", allowEmpty: true},
		{name: "空值不允许", raw: "", wantErr: true},
		{name: "缺少协议失败", raw: "127.0.0.1:8096", wantErr: true},
		{name: "FTP 失败", raw: "ftp://example.com", wantErr: true},
		{name: "省略主机名通过", raw: "http://:8096"},
		{name: "端口边界通过", raw: "http://emby.example.com:65535"},
		{name: "端口超范围失败", raw: "https://emby.example.com:99999", wantErr: true},
		{name: "端口为零失败", raw: "http://emby.example.com:0", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := HTTPURL("strm_base_url", tt.raw, tt.allowEmpty)
			if (err != nil) != tt.wantErr {
				t.Fatalf("HTTPURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCron(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{name: "每小时整点", expr: "0 * * * *"},
		{name: "每天凌晨两点", expr: "0 2 * * *"},
		{name: "每十分钟", expr: "*/10 * * * *"},
		{name: "描述符 hourly", expr: "@hourly"},
		{name: "描述符 every", expr: "@every 1h30m"},
		{name: "六位秒级不支持", expr: "0 0 2 * * *", wantErr: true},
		{name: "Quartz 问号不支持", expr: "0 0 2 ? * *", wantErr: true},
		{name: "Quartz L 不支持", expr: "0 0 2 L * *", wantErr: true},
		{name: "Quartz W 不支持", expr: "0 0 2 1W * *", wantErr: true},
		{name: "Quartz # 不支持", expr: "0 0 2 ? * 1#1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Cron("cron", tt.expr, false)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Cron(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
			if tt.wantErr && err.Error() != "cron：仅支持 5 位 cron 表达式或 robfig 描述符" {
				t.Fatalf("Cron(%q) error = %q, want explicit format message", tt.expr, err.Error())
			}
		})
	}

	if err := Cron("cron", "", true); err != nil {
		t.Fatalf("Cron() allow empty error = %v", err)
	}
	if err := Cron("cron", "", false); err == nil {
		t.Fatal("Cron() required empty expected error")
	}
}

func TestExtList(t *testing.T) {
	if err := ExtList("video_ext_arr", []string{".mp4", ".mkv"}, false); err != nil {
		t.Fatalf("ExtList() error = %v", err)
	}
	if err := ExtList("video_ext_arr", []string{"mp4"}, false); err == nil {
		t.Fatal("ExtList() expected missing dot error")
	}
	if err := ExtList("video_ext_arr", nil, true); err != nil {
		t.Fatalf("ExtList() allow empty error = %v", err)
	}
}

func TestLength(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		min     int
		max     int
		wantErr bool
	}{
		{name: "合法长度通过", value: "账号名称", min: 1, max: 64},
		{name: "空白失败", value: " ", min: 1, max: 64, wantErr: true},
		{name: "过长失败", value: "abcdef", min: 1, max: 5, wantErr: true},
		{name: "控制字符失败", value: "bad\nname", min: 1, max: 64, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Length("name", tt.value, tt.min, tt.max)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Length() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPositiveID(t *testing.T) {
	if err := PositiveID("id", 1); err != nil {
		t.Fatalf("PositiveID() error = %v", err)
	}
	if err := PositiveID("id", 0); err == nil {
		t.Fatal("PositiveID() expected error")
	}
}

func TestProxyURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		allowEmpty bool
		wantErr    bool
	}{
		{name: "HTTP 通过", raw: "http://127.0.0.1:7890"},
		{name: "HTTPS 通过", raw: "https://proxy.example.com"},
		{name: "SOCKS5 通过", raw: "socks5://127.0.0.1:1080"},
		{name: "SOCKS5H 通过", raw: "socks5h://127.0.0.1:1080"},
		{name: "带认证的 SOCKS5 通过", raw: "socks5://user:pass@127.0.0.1:1080"},
		{name: "IPv6 通过", raw: "socks5://[::1]:1080"},
		{name: "首尾空白通过", raw: "  socks5://127.0.0.1:1080  "},
		{name: "允许空值", raw: "", allowEmpty: true},
		{name: "缺少协议失败", raw: "127.0.0.1:7890", wantErr: true},
		{name: "SOCKS4 失败", raw: "socks4://127.0.0.1:1080", wantErr: true},
		// 省略主机名是本地代理的常用简写，Go 会解析为本机，实测经它出站可拿到 200，不能拒绝。
		{name: "省略主机名的 HTTP 通过", raw: "http://:1080"},
		{name: "省略主机名的 SOCKS5 通过", raw: "socks5://:1080"},
		{name: "只有协议失败", raw: "socks5://", wantErr: true},
		{name: "端口边界通过", raw: "socks5://127.0.0.1:65535"},
		{name: "端口超范围失败", raw: "socks5://127.0.0.1:99999", wantErr: true},
		{name: "端口为零失败", raw: "socks5://127.0.0.1:0", wantErr: true},
		{name: "省略主机名端口超范围失败", raw: "http://:99999", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ProxyURL("http_proxy", tt.raw, tt.allowEmpty)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ProxyURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// TestProxyURLPortMessage 端口越界必须给出端口专属提示，
// 否则用户会照着"格式无效"去改协议而不是改端口。
func TestProxyURLPortMessage(t *testing.T) {
	err := ProxyURL("http_proxy", "socks5://127.0.0.1:99999", false)
	if err == nil {
		t.Fatal("ProxyURL() 期望端口越界报错")
	}
	if !strings.Contains(err.Error(), "端口必须在 1-65535 之间") {
		t.Fatalf("ProxyURL() error = %q，期望包含端口范围提示", err.Error())
	}
}

// TestProxySchemeSupported 锁定传输层共用的白名单入口。
// 协议来自 url.Parse，它会统一转小写，因此这里只覆盖小写形态。
func TestProxySchemeSupported(t *testing.T) {
	for _, scheme := range []string{"http", "https", "socks5", "socks5h"} {
		if !ProxySchemeSupported(scheme) {
			t.Fatalf("期望协议 %q 受支持，实际被拒绝", scheme)
		}
	}
	for _, scheme := range []string{"socks4", "socks", "ftp", ""} {
		if ProxySchemeSupported(scheme) {
			t.Fatalf("期望协议 %q 被拒绝，实际受支持", scheme)
		}
	}
}

// TestProxySchemeHintCoversWhitelist 确认提示文案随白名单生成，新增协议时不会漏改文案。
func TestProxySchemeHintCoversWhitelist(t *testing.T) {
	for _, scheme := range proxySchemes {
		if !strings.Contains(ProxySchemeHint, scheme) {
			t.Fatalf("提示文案 %q 未包含协议 %q", ProxySchemeHint, scheme)
		}
	}
}

func TestDownloadProxyURL(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{name: "115 CDN 域名通过", raw: "https://cdn.115cdn.net/file.mp4"},
		{name: "百度 PCS 域名通过", raw: "https://d.pcs.baidu.com/file.mp4"},
		{name: "百度 PCS 子域通过", raw: "https://foo.baidupcs.com/file.mp4"},
		{name: "显式端口通过", raw: "https://cdn.115cdn.net:443/file.mp4"},
		{name: "空值失败", raw: "", wantErr: true},
		{name: "非 HTTP 失败", raw: "ftp://cdn.115cdn.net/file.mp4", wantErr: true},
		{name: "未知域名失败", raw: "https://example.com/file.mp4", wantErr: true},
		{name: "端口超范围失败", raw: "https://115cdn.net:99999/file.mp4", wantErr: true},
		{name: "端口为零失败", raw: "https://d.pcs.baidu.com:0/file.mp4", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := DownloadProxyURL("url", tt.raw)
			if (err != nil) != tt.wantErr {
				t.Fatalf("DownloadProxyURL() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
