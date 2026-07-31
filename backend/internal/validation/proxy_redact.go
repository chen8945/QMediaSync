package validation

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// 代理地址的脱敏与解析错误处理放在本包，而不是 helpers：
// helpers/net.go 已经导入 github，github 无法反向引用 helpers，两边各留一份实现必然漂移。
// validation 是纯 stdlib 叶子包，helpers、github、models、controllers 都能直接引用。

const (
	// proxyCredentialMask 替换凭据的占位串，沿用标准库 Redacted() 的写法，便于日志中辨认。
	proxyCredentialMask = "xxxxx"
	// proxyUnparsablePlaceholder 解析失败时的兜底文案。此时无法判断哪一段是凭据，绝不能回显原串。
	proxyUnparsablePlaceholder = "<代理地址无法解析>"
)

// RedactProxyURL 遮蔽代理地址中的凭据，供日志与接口回显使用。
//
// 不能直接用 url.URL.Redacted()：它只替换密码，用户名仍是明文（企业代理常带域账号，
// 形如 http://DOMAIN\jsmith:pw@proxy.corp:8080），且对缺少 // 的 opaque 地址
// （形如 socks5:user:secret@host:1080）完全不生效，会把密码原样输出。
func RedactProxyURL(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return proxyUnparsablePlaceholder
	}
	return RedactParsedProxyURL(parsed)
}

// RedactParsedProxyURL 与 RedactProxyURL 等价，供已经解析过地址的调用方复用，避免二次解析。
// 入参不会被修改。
func RedactParsedProxyURL(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	masked := *parsed
	// opaque 地址不会填充 User，凭据留在 Opaque 里，按最后一个 @ 切分以保留主机便于排查。
	if masked.Opaque != "" {
		if at := strings.LastIndex(masked.Opaque, "@"); at >= 0 {
			masked.Opaque = proxyCredentialMask + masked.Opaque[at:]
		}
		return masked.String()
	}
	if masked.User != nil {
		if _, hasPassword := masked.User.Password(); hasPassword {
			masked.User = url.UserPassword(proxyCredentialMask, proxyCredentialMask)
		} else {
			masked.User = url.User(proxyCredentialMask)
		}
	}
	return masked.String()
}

// ProxyParseError 剥掉 url.Error 中回显的原始地址。
// url.Error.Error() 会打印 parse "<原串>"，代理地址常带用户名密码，直接外抛会把凭据写进日志和接口响应。
func ProxyParseError(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return fmt.Errorf("代理 URL 格式无效：%v", urlErr.Err)
	}
	return fmt.Errorf("代理 URL 格式无效：%v", err)
}
