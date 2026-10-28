package playback

import (
	"strings"
	"testing"
)

func TestEmbyAuthAttributes(t *testing.T) {
	for _, test := range []struct {
		name, header, device, client, token string
		valid                               bool
	}{
		{"Yamby-shape", `Emby UserId=user,Client=Yamby,Device=Android Phone,DeviceId=opaque-device,Version=2.1.1.0`, "opaque-device", "Yamby", "", true},
		{"Hills-shape", `Emby Client="Hills", Device="Phone", DeviceId="aBc123", Version="1.10.1"`, "aBc123", "Hills", "", true},
		{"spaces-case-order", `mEdIaBrOwSeR vErSiOn=1, DEVICEID = "opaque / Id_1", TOKEN = value, ClIeNt = Custom Player`, "opaque / Id_1", "Custom Player", "value", true},
		{"escaped-unknown", `Emby Extra="a,b", Client="a\"b\\c", DeviceId=id, Token=one`, "id", `a"b\c`, "one", true},
		{"no-scheme", `DeviceId=id, Token=one`, "id", "", "one", true},
		{"bare-native", "native-session==", "", "", "", true},
		{"bearer", "Bearer native-session==", "", "", "", true},
		{"unclosed", `Emby DeviceId="unfinished`, "", "", "", false},
		{"quoted-suffix", `Emby DeviceId="id"trailing`, "", "", "", false},
		{"controls", "Emby DeviceId=bad\x01id", "", "", "", false},
		{"oversized", `Emby DeviceId="` + strings.Repeat("x", 8192) + `"`, "", "", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			attributes, valid := EmbyAuthAttributes(test.header)
			if valid != test.valid || valid && (attributes.Get("deviceid") != test.device || attributes.Get("client") != test.client || attributes.Get("token") != test.token) {
				t.Fatal("认证描述解析结果不符")
			}
		})
	}
	attributes, valid := EmbyAuthAttributes(`Emby DeviceId=A, deviceid=B`)
	if !valid || len(attributes["deviceid"]) != 2 {
		t.Fatal("重复属性被隐藏，调用方无法核对冲突")
	}
}
