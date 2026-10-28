package playback

import (
	"net/url"
	"strings"
)

// EmbyAuthAttributes 解析客户端认证描述；字段名忽略大小写，字段值保持原样。
// 裸会话和 Bearer 不属于属性描述，仍交给调用方按原凭据规则处理。
func EmbyAuthAttributes(value string) (url.Values, bool) {
	if len(value) > 8192 || strings.ContainsAny(value, "\r\n\x00") {
		return nil, false
	}
	value = strings.TrimSpace(value)
	if end := strings.IndexAny(value, " \t"); end >= 0 && (strings.EqualFold(value[:end], "Emby") || strings.EqualFold(value[:end], "MediaBrowser")) {
		value = strings.TrimSpace(value[end:])
	} else {
		name, _, found := strings.Cut(value, "=")
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "token", "deviceid", "client", "device", "version", "userid":
			if !found {
				return nil, true
			}
		default:
			return nil, true
		}
	}
	attributes := make(url.Values)
	for count := 0; value != ""; count++ {
		if count >= 32 {
			return nil, false
		}
		name, rest, found := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		if !found || name == "" {
			return nil, false
		}
		for _, c := range name {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return nil, false
			}
		}
		rest = strings.TrimLeft(rest, " \t")
		var field string
		if strings.HasPrefix(rest, `"`) {
			var decoded strings.Builder
			closed := false
			for i := 1; i < len(rest); i++ {
				c := rest[i]
				if c == '"' {
					field, rest, closed = decoded.String(), rest[i+1:], true
					break
				}
				if c == '\\' {
					i++
					if i == len(rest) {
						return nil, false
					}
					c = rest[i]
				}
				decoded.WriteByte(c)
			}
			if !closed {
				return nil, false
			}
			rest = strings.TrimLeft(rest, " \t")
			if rest != "" && rest[0] != ',' {
				return nil, false
			}
		} else {
			field, rest, _ = strings.Cut(rest, ",")
			field = strings.TrimSpace(field)
			if strings.ContainsRune(field, '"') {
				return nil, false
			}
			if rest != "" {
				rest = "," + rest
			}
		}
		if len(field) > 4096 {
			return nil, false
		}
		for _, c := range field {
			if c < 0x20 || c == 0x7f {
				return nil, false
			}
		}
		attributes.Add(strings.ToLower(name), field)
		value = strings.TrimSpace(strings.TrimPrefix(rest, ","))
	}
	return attributes, true
}
