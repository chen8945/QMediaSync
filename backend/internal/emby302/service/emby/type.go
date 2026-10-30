package emby

import (
	"encoding/json"
	"fmt"

	"qmediasync/internal/helpers"
)

// MsInfo MediaSourceId 解析信息
type MsInfo struct {
	Empty            bool   // 传递的 ID 是否为空值
	Transcode        bool   // 是否请求转码的资源
	OriginId         string // 原始 MediaSourceId
	RawId            string // 未解析的原始请求 ID
	TemplateId       string // OpenList 中转码资源的模板 ID
	Format           string // 转码资源的格式, 比如：1920x1080
	SourceNamePrefix string // 转码资源名称前缀
	OpenlistPath     string // 资源在 OpenList 中的地址
}

// String 序列化输出
func (mi MsInfo) String() string {
	return fmt.Sprintf("MsInfo{Empty: [%v], Transcode: [%v], OriginId: [%v], RawId: [%v], TemplateId: [%v], Format: [%v], SourceNamePrefix: [%v], OpenlistPath: [%v]}",
		mi.Empty, mi.Transcode, mi.OriginId, mi.RawId, mi.TemplateId, mi.Format, mi.SourceNamePrefix, mi.OpenlistPath)
}

// RouteType 接口路由类型
type RouteType string

const (
	RouteItems        RouteType = "Items"
	RoutePlaybackInfo RouteType = "PlaybackInfo"
	RouteStream       RouteType = "Stream"
	RouteSyncDownload RouteType = "SyncDownload"
	RouteTranscode    RouteType = "Transcode"
	RouteOriginal     RouteType = "Original"
)

// ItemInfo Emby 资源 Item 解析信息
type ItemInfo struct {
	Id              string     // Item ID
	MsInfo          MsInfo     // MediaSourceId 解析信息
	ApiKey          string     // Emby 接口密钥
	ApiKeyType      ApiKeyType // Emby 接口密钥类型
	ApiKeyName      string     // Emby 接口密钥名称
	PlaybackInfoUri string     // Item 信息查询接口 URI, 通过源服务器查询
	RouteType
}

// String 序列化输出
func (ii ItemInfo) String() string {
	return ii.formatString(false)
}

// SensitiveString 序列化输出完整敏感信息，仅用于显式 unsafe Debug 日志。
func (ii ItemInfo) SensitiveString() string {
	return ii.formatString(true)
}

func (ii ItemInfo) formatString(includeSensitive bool) string {
	apiKey := ii.ApiKey
	playbackInfoURI := ii.PlaybackInfoUri
	if !includeSensitive {
		if apiKey != "" {
			apiKey = helpers.SensitiveLogMask
		}
		playbackInfoURI = helpers.RedactSensitiveLog(playbackInfoURI)
	}
	return fmt.Sprintf("ItemInfo{Id: [%s], MsInfo: [%v], ApiKey: [%s], ApiKeyType: [%s], ApiKeyName: [%s], PlaybackInfoUri: [%s], RouteType: [%s]}",
		ii.Id, ii.MsInfo, apiKey, ii.ApiKeyType, ii.ApiKeyName, playbackInfoURI, ii.RouteType)
}

// ItemsHolder Emby Items 接口响应接收结构
type ItemsHolder struct {
	Items            []json.RawMessage
	TotalRecordCount int `json:",omitempty"`
}
