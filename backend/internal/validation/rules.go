package validation

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
)

func NonBlank(field string, value string) error {
	if strings.TrimSpace(value) == "" {
		return New(field, "不能为空")
	}
	return nil
}

func RangeInt(field string, value int, min int, max int) error {
	if value < min || value > max {
		return New(field, "取值超出允许范围")
	}
	return nil
}

func RangeInt64(field string, value int64, min int64, max int64) error {
	if value < min || value > max {
		return New(field, "取值超出允许范围")
	}
	return nil
}

func OneOfInt(field string, value int, allowed []int) error {
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return New(field, "不是允许的取值")
}

func OneOfString(field string, value string, allowed []string) error {
	for _, item := range allowed {
		if value == item {
			return nil
		}
	}
	return New(field, "不是允许的取值")
}

func HTTPURL(field string, raw string, allowEmpty bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if allowEmpty {
			return nil
		}
		return New(field, "不能为空")
	}
	parsed, err := url.Parse(raw)
	// 只要求 Host 非空，不额外要求主机名非空：形如 http://:8096 的地址 Go 会当作本机，
	// 实测可以正常发出请求，收紧会误拒已经在用的本机地址。
	if err != nil || parsed.Host == "" {
		return New(field, "必须是有效的 HTTP URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return New(field, "只支持 http 或 https")
	}
	return PortInRange(field, parsed)
}

func ExtList(field string, values []string, allowEmpty bool) error {
	if len(values) == 0 {
		if allowEmpty {
			return nil
		}
		return New(field, "不能为空")
	}
	for _, value := range values {
		item := strings.TrimSpace(value)
		if item == "" {
			return New(field, "不能包含空值")
		}
		if strings.ContainsAny(item, " \t\r\n") {
			return New(field, "不能包含空白字符")
		}
		if !strings.HasPrefix(item, ".") {
			return New(field, "扩展名必须以 . 开头")
		}
	}
	return nil
}

func Length(field string, value string, min int, max int) error {
	value = strings.TrimSpace(value)
	if len([]rune(value)) < min || len([]rune(value)) > max {
		return New(field, "长度超出允许范围")
	}
	for _, r := range value {
		if r < 32 || r == 127 {
			return New(field, "不能包含控制字符")
		}
	}
	return nil
}

func PositiveID(field string, value uint) error {
	if value == 0 {
		return New(field, "必须大于 0")
	}
	return nil
}

// proxySchemes 出站代理支持的协议。SOCKS5 由 Go net/http 的 Transport 原生拨号，socks5 与 socks5h 等价，域名都交给代理端解析。
// 这里是唯一来源，传输层通过 ProxySchemeSupported 和 ProxySchemeHint 共用，避免两层白名单各自漂移。
var proxySchemes = []string{"http", "https", "socks5", "socks5h"}

// ProxySchemeHint 协议不受支持时的统一提示文案，随 proxySchemes 自动生成。
var ProxySchemeHint = "只支持 " + strings.Join(proxySchemes, "、")

// ProxySchemeSupported 判断代理协议是否在白名单内。
// 传入的协议应当来自 url.Parse，它会将 scheme 统一转为小写。
func ProxySchemeSupported(scheme string) bool {
	return slices.Contains(proxySchemes, scheme)
}

// PortInRange 校验 URL 中显式给出的端口落在 1-65535。
// url.Parse 只保证端口是数字，65536 甚至 0 都能解析通过，因此所有 URL 规则都要显式补这一层。
// 没写端口时由协议默认端口决定，不做检查。
func PortInRange(field string, parsed *url.URL) error {
	port := parsed.Port()
	if port == "" {
		return nil
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return New(field, "端口必须在 1-65535 之间")
	}
	return nil
}

func ProxyURL(field string, raw string, allowEmpty bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		if allowEmpty {
			return nil
		}
		return New(field, "不能为空")
	}
	parsed, err := url.Parse(raw)
	// 用 Host 而非 Hostname：形如 http://:1080 的地址主机名为空，但 Go 会解析为本机，
	// 实测经它出站可以拿到 200 响应，是本地代理的常用简写（输入框占位符就是 127.0.0.1:7890），必须放行。
	// Host 为空则说明连目标地址都没有，例如 socks5:// 或 localhost:1080 这类漏写 // 的输入。
	if err != nil || parsed.Host == "" {
		return New(field, "必须是有效的代理 URL")
	}
	if !ProxySchemeSupported(parsed.Scheme) {
		return New(field, ProxySchemeHint)
	}
	return PortInRange(field, parsed)
}

func DownloadProxyURL(field string, raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return New(field, "不能为空")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return New(field, "必须是有效的网盘下载 URL")
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return New(field, "只支持 http 或 https")
	}
	// 这里是文档明确的反代安全边界，端口范围要和域名白名单一起收口。
	if err := PortInRange(field, parsed); err != nil {
		return err
	}
	if host == "d.pcs.baidu.com" || isHostOrSubdomain(host, "115cdn.net") || isHostOrSubdomain(host, "baidupcs.com") {
		return nil
	}
	return New(field, "只允许 115 或百度网盘下载域名")
}

func isHostOrSubdomain(host string, domain string) bool {
	return host == domain || strings.HasSuffix(host, "."+domain)
}
