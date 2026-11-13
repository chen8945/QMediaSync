package controllers

import (
	"strings"
	"testing"

	"qmediasync/internal/models"
	"qmediasync/internal/validation"
)

func TestHTTPProxyCredentialIntent(t *testing.T) {
	const stored = "socks5://user:secret@10.0.0.5:1080"
	masked := validation.RedactProxyURL(stored)
	preserve, replace := true, false
	for _, tc := range []struct {
		name, submitted, current, want string
		preserve                       *bool
	}{
		{"脱敏回传显式保留", masked, stored, stored, &preserve},
		{"旧调用方脱敏回传兼容", masked, stored, stored, nil},
		{"主动更换xxxxx凭据", masked, stored, masked, &replace},
		{"更换无凭据代理", "http://127.0.0.1:7890", stored, "http://127.0.0.1:7890", &replace},
		{"更换凭据", "socks5://alice:pw@10.0.0.5:1080", stored, "socks5://alice:pw@10.0.0.5:1080", &replace},
		{"协议改变不转发旧凭据", "http://xxxxx:xxxxx@10.0.0.5:1080", stored, "http://xxxxx:xxxxx@10.0.0.5:1080", &preserve},
		{"主机改变不转发旧凭据", "socks5://xxxxx:xxxxx@10.0.0.9:1080", stored, "socks5://xxxxx:xxxxx@10.0.0.9:1080", &preserve},
		{"端口改变不转发旧凭据", "socks5://xxxxx:xxxxx@10.0.0.5:1081", stored, "socks5://xxxxx:xxxxx@10.0.0.5:1081", &preserve},
		{"清空优先于保留", "", stored, "", &preserve},
		{"无凭据原值", "http://127.0.0.1:7890", "http://127.0.0.1:7890", "http://127.0.0.1:7890", &preserve},
		{"尚无存储配置", stored, "", stored, &preserve},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := models.ResolveHTTPProxy(tc.submitted, tc.current, tc.preserve); got != tc.want {
				t.Fatalf("代理=%q，期望%q", got, tc.want)
			}
		})
	}
	if strings.Contains(masked, "secret") || strings.Contains(masked, "user") || !strings.Contains(masked, "10.0.0.5:1080") {
		t.Fatal("接口脱敏必须隐藏凭据并保留端点")
	}
}
