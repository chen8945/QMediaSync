package v115auth

import "testing"

func TestBuiltInAppIDOrder(t *testing.T) {
	sources := BuiltInAppIDSources()
	if sources[0].AppName != "QMediaSync" {
		t.Fatalf("第一个内置 APP ID = %s，期望 QMediaSync", sources[0].AppName)
	}
	if sources[0].SourceType != SourceTypeBuiltInAppID {
		t.Fatalf("来源类型 = %s，期望 %s", sources[0].SourceType, SourceTypeBuiltInAppID)
	}
	if sources[0].Provider != ProviderOfficialPKCE {
		t.Fatalf("授权 provider = %s，期望 %s", sources[0].Provider, ProviderOfficialPKCE)
	}
	if sources[0].RequiresEncryptionKey {
		t.Fatal("内置 APP ID 不应要求共享 OAUTH_RELAY_ENCRYPTION_KEY")
	}
	if !sources[0].Pinned || sources[0].Deprecated {
		t.Fatal("QMediaSync 应置顶且不应标记为弃用")
	}
	for _, source := range sources {
		if source.AppName == "Q115-STRM" || source.AppName == "MQ的媒体库" {
			t.Fatalf("已移除的内置 APP ID 仍出现在可选目录: %s", source.AppName)
		}
	}
}

func TestBuiltInAppIDCatalogCompleteness(t *testing.T) {
	sources := BuiltInAppIDSources()
	if len(sources) != 1039 {
		t.Fatalf("内置 APP ID 可选目录数量 = %d，期望 1039", len(sources))
	}

	seen := make(map[string]struct{}, len(sources))
	for _, source := range sources {
		if _, ok := seen[source.AppID]; ok {
			t.Fatalf("内置 APP ID 目录存在重复项: %s", source.AppID)
		}
		seen[source.AppID] = struct{}{}
	}

	if _, ok := seen["100197925"]; !ok {
		t.Fatal("内置 APP ID 目录缺少 p115client OPEN_APP_IDS 尾部项 100197925")
	}
}

func TestSearchBuiltInAppIDSourcesFindsTailAppID(t *testing.T) {
	result := SearchBuiltInAppIDSources("100197925", 0, 50)
	if result.Total != 1 {
		t.Fatalf("搜索尾部 APP ID 总数 = %d，期望 1", result.Total)
	}
	if len(result.Items) != 1 {
		t.Fatalf("搜索尾部 APP ID 返回数量 = %d，期望 1", len(result.Items))
	}
	if result.Items[0].AppID != "100197925" {
		t.Fatalf("搜索尾部 APP ID 返回 %s，期望 100197925", result.Items[0].AppID)
	}
	if result.Items[0].AppName != "自己文件处理2" {
		t.Fatalf("搜索尾部 APP ID 名称 = %s，期望 自己文件处理2", result.Items[0].AppName)
	}
}

func TestSearchBuiltInAppIDSourcesReturnsCatalogOrder(t *testing.T) {
	result := SearchBuiltInAppIDSources("", 0, 3)
	if result.Total != 1039 {
		t.Fatalf("可搜索内置 APP ID 总数 = %d，期望 1039", result.Total)
	}
	want := []string{"100195125", "100195127", "100195129"}
	if len(result.Items) != len(want) {
		t.Fatalf("可搜索内置 APP ID 返回数量 = %d，期望 %d", len(result.Items), len(want))
	}
	for i, appID := range want {
		if result.Items[i].AppID != appID {
			t.Fatalf("第 %d 个可搜索内置 APP ID = %s，期望 %s", i, result.Items[i].AppID, appID)
		}
	}
}

func TestSearchBuiltInAppIDSourcesFindsPinnedApp(t *testing.T) {
	result := SearchBuiltInAppIDSources("QMediaSync", 0, 50)
	if result.Total != 1 || len(result.Items) != 1 {
		t.Fatalf("搜索置顶应用 QMediaSync 返回了异常结果: %#v", result)
	}
	if result.Items[0].AppID != "100197849" || !result.Items[0].Pinned {
		t.Fatalf("搜索置顶应用 QMediaSync 返回了异常应用: %#v", result.Items[0])
	}
}

func TestSearchBuiltInAppIDSourcesExcludesDeprecatedApps(t *testing.T) {
	for _, keyword := range []string{"Q115-STRM", "MQ的媒体库"} {
		result := SearchBuiltInAppIDSources(keyword, 0, 50)
		if result.Total != 0 || len(result.Items) != 0 {
			t.Fatalf("搜索已移除应用 %q 返回了结果: %#v", keyword, result)
		}
	}
}

func TestSourceFromCreateRequestRejectsDeprecatedBuiltInAppIDs(t *testing.T) {
	for _, appID := range []string{"100197665", "100197503"} {
		if _, err := SourceFromCreateRequest(SourceTypeBuiltInAppID, ProviderOfficialPKCE, appID, "", ""); err == nil {
			t.Fatalf("弃用的内置 APP ID %s 不应允许新建账号", appID)
		}
	}
	for _, appID := range []string{BuiltInRelayQ115STRM, BuiltInRelayMQMediaLibrary} {
		if _, err := SourceFromCreateRequest(SourceTypeBuiltInRelay, ProviderMQFamily, "", appID, ""); err == nil {
			t.Fatalf("弃用的内置中转 %s 不应允许新建账号", appID)
		}
	}
}

func TestLegacyCreateRequestKeepsDeprecatedRelayParsing(t *testing.T) {
	for _, appID := range []string{BuiltInRelayQ115STRM, BuiltInRelayMQMediaLibrary} {
		source, err := SourceFromCreateRequest("", "", "", appID, "")
		if err != nil {
			t.Fatalf("旧内置中转字符串 %s 解析失败: %v", appID, err)
		}
		if source.Provider != ProviderMQFamily || !source.Deprecated {
			t.Fatalf("旧内置中转字符串 %s 返回了异常来源: %#v", appID, source)
		}
	}
}

func TestResolveAccountSourceKeepsDeprecatedBuiltInAppIDForHistory(t *testing.T) {
	for _, appID := range []string{"100197665", "100197503"} {
		source := ResolveAccountSource(appID, "")
		if source.AppID != appID || !source.Deprecated {
			t.Fatalf("历史账号 APP ID %s 未保留弃用来源信息: %#v", appID, source)
		}
	}
}

func TestKnownThirdPartySources(t *testing.T) {
	cases := map[AuthProvider]string{
		ProviderMoviePilot: "100197847",
		ProviderCloudDrive: "100195313",
	}
	for provider, appID := range cases {
		source, ok := FindSource(SourceTypeThirdPartyService, provider, appID)
		if !ok {
			t.Fatalf("未找到第三方来源 %s", provider)
		}
		if source.AppID != appID {
			t.Fatalf("%s APP ID = %s，期望 %s", provider, source.AppID, appID)
		}
		if source.RequiresEncryptionKey {
			t.Fatalf("%s 不应要求共享 OAUTH_RELAY_ENCRYPTION_KEY", provider)
		}
	}
}

func TestResolveLegacyRelayAccount(t *testing.T) {
	cases := []struct {
		appID    string
		provider AuthProvider
	}{
		{"QMediaSync", ProviderQMediaSync},
		{"Q115-STRM", ProviderMQFamily},
		{"MQ的媒体库", ProviderMQFamily},
	}
	for _, tt := range cases {
		source := ResolveAccountSource(tt.appID, "")
		if source.SourceType != SourceTypeBuiltInRelay {
			t.Fatalf("%s 来源类型 = %s，期望 %s", tt.appID, source.SourceType, SourceTypeBuiltInRelay)
		}
		if source.Provider != tt.provider {
			t.Fatalf("%s provider = %s，期望 %s", tt.appID, source.Provider, tt.provider)
		}
		if source.Deprecated != (tt.provider == ProviderMQFamily) {
			t.Fatalf("%s 弃用标记 = %t，期望 %t", tt.appID, source.Deprecated, tt.provider == ProviderMQFamily)
		}
	}
}

func TestResolveNumericAppIDAccount(t *testing.T) {
	source := ResolveAccountSource("100197849", "QMediaSync")
	if source.SourceType != SourceTypeBuiltInAppID {
		t.Fatalf("来源类型 = %s，期望 %s", source.SourceType, SourceTypeBuiltInAppID)
	}
	if source.Provider != ProviderOfficialPKCE {
		t.Fatalf("provider = %s，期望 %s", source.Provider, ProviderOfficialPKCE)
	}
}

func TestCustomAppIDDefaultDisplayName(t *testing.T) {
	source := ResolveAccountSource("custom-app-id", "")
	if source.AppName != "自定义 APP ID" {
		t.Fatalf("自定义 APP ID 默认应用名 = %s，期望 自定义 APP ID", source.AppName)
	}
	if source.DisplayName != "自定义 APP ID" {
		t.Fatalf("自定义 APP ID 默认展示名 = %s，期望 自定义 APP ID", source.DisplayName)
	}
}

func TestLegacyCustomAppIDNames(t *testing.T) {
	tests := []struct {
		name        string
		selectedApp string
	}{
		{name: "旧 App ID 写法", selectedApp: "自定义 App ID"},
		{name: "旧 APPID 写法", selectedApp: "自定义 APPID"},
		{name: "新 APP ID 写法", selectedApp: "自定义 APP ID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source, err := SourceFromCreateRequest("", "", "custom-app-id", tt.selectedApp, "")
			if err != nil {
				t.Fatalf("SourceFromCreateRequest() error = %v", err)
			}
			if source.AppName != "自定义 APP ID" {
				t.Fatalf("自定义 APP ID 应默认保存为新展示名，got %s", source.AppName)
			}
		})
	}
}
