package models

import (
	"reflect"
	"testing"
)

func TestSettingStrmRegexRoundTrip(t *testing.T) {
	patterns := []string{"(?i)^Sample\\.[^.]+$", "^A{1,3};B$", " \\QAbC\\E "}
	setting := SettingStrm{
		ExcludeNameArr:      []string{"SaMpLe"},
		ExcludeNameRegexArr: append([]string(nil), patterns...),
	}
	encoded := setting.EncodeArr()
	if encoded == nil {
		t.Fatal("编码 STRM 设置失败")
	}
	if !reflect.DeepEqual(encoded.ExcludeNameArr, []string{"sample"}) {
		t.Fatalf("原排除名称应继续转小写，实际为 %q", encoded.ExcludeNameArr)
	}
	stored := encoded.ToMap(true, true)
	decoded := (SettingStrm{
		ExcludeName:      stored["exclude_name"].(string),
		ExcludeNameRegex: stored["exclude_name_regex"].(string),
	}).DecodeArr(true)
	if decoded == nil {
		t.Fatal("解码 STRM 设置失败")
	}
	if !reflect.DeepEqual(decoded.ExcludeNameRegexArr, patterns) {
		t.Fatalf("正则落库再加载后变为 %q，期望原文 %q", decoded.ExcludeNameRegexArr, patterns)
	}
	if got := decoded.ToMap(false, true)["exclude_name_regex_arr"]; !reflect.DeepEqual(got, patterns) {
		t.Fatalf("API 正则数组 = %q，期望 %q", got, patterns)
	}
}

func TestSettingStrmLegacyRegexDefaultsToEmpty(t *testing.T) {
	for _, isSetting := range []bool{false, true} {
		name := "同步目录"
		if isSetting {
			name = "全局设置"
		}
		t.Run(name, func(t *testing.T) {
			decoded := (SettingStrm{ExcludeName: "[\"sample\"]"}).DecodeArr(isSetting)
			if decoded == nil || decoded.ExcludeNameRegexArr == nil || len(decoded.ExcludeNameRegexArr) != 0 {
				t.Fatalf("旧设置应返回空正则数组，实际为 %+v", decoded)
			}
			if !reflect.DeepEqual(decoded.ExcludeNameArr, []string{"sample"}) {
				t.Fatalf("旧排除名称被修改：%q", decoded.ExcludeNameArr)
			}
		})
	}
}

func TestSyncPathRegexInheritance(t *testing.T) {
	originalSettings := SettingsGlobal
	t.Cleanup(func() { SettingsGlobal = originalSettings })
	SettingsGlobal = &Settings{SettingStrm: SettingStrm{
		ExcludeNameRegexArr: []string{"(?i)global"},
	}}
	path := &SyncPath{CustomConfig: true, SettingStrm: GetStrmSettingDefault()}
	if got := path.GetExcludeNameRegexArr(); !reflect.DeepEqual(got, []string{"(?i)global"}) {
		t.Fatalf("空自定义列表应继承全局，实际为 %q", got)
	}
	path.ExcludeNameRegexArr = []string{"^Custom$"}
	if got := path.GetExcludeNameRegexArr(); !reflect.DeepEqual(got, []string{"^Custom$"}) {
		t.Fatalf("非空自定义列表应覆盖全局，实际为 %q", got)
	}
}
