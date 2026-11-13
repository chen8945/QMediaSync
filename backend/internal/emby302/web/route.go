package web

import (
	"qmediasync/internal/emby302/constant"
	"qmediasync/internal/emby302/service/emby"
	"qmediasync/internal/emby302/service/m3u8"

	"github.com/gin-gonic/gin"
)

func rulePatterns(session *emby.Session, playlists *m3u8.Store) [][2]any {
	return compileRules([][2]any{
		// WebSocket
		{constant.Reg_Socket, session.ProxySocket()},

		// PlaybackInfo 接口
		{constant.Reg_PlaybackInfo, session.TransferPlaybackInfo},

		// 播放停止时, 辅助请求 Progress 记录进度
		{constant.Reg_PlayingStopped, session.PlayingStoppedHelper},
		// 拦截无效的进度报告
		{constant.Reg_PlayingProgress, session.PlayingProgressHelper},

		// Items 接口
		{constant.Reg_UserItems, session.LoadCacheItems},
		// 代理 Items 并添加转码版本信息
		{constant.Reg_UserEpisodeItems, session.ProxyAddItemsPreviewInfo},
		// 随机列表接口
		{constant.Reg_UserItemsRandomResort, session.ResortRandomItems},
		// 代理原始的随机列表接口, 去除 Limit 限制并缓存
		{constant.Reg_UserItemsRandomWithLimit, session.RandomItemsWithLimit},
		// 代理 Latest 接口, 解码媒体的 Path 字段
		{constant.Reg_UserLatestItems, session.ProxyLatestItems},

		// 重排序剧集
		{constant.Reg_ShowEpisodes, session.ResortEpisodes},

		// 字幕长时间缓存
		{constant.Reg_VideoSubtitles, session.ProxySubtitles},

		// 资源重定向到直链
		{constant.Reg_ResourceStream, session.Redirect2OpenlistLink},
		// master 重定向到本地 M3U8 代理
		{constant.Reg_ResourceMaster, session.Redirect2Transcode},
		// main 路由到直链接口
		{constant.Reg_ResourceMain, session.Redirect2Transcode},
		// 处理 original 资源
		{constant.Reg_ResourceOriginal, session.ProxyOriginalResource},
		// M3U8 转码播放列表
		{constant.Reg_ProxyPlaylist, playlists.ProxyPlaylist},
		// TS 重定向到直链
		{constant.Reg_ProxyTs, playlists.ProxyTsLink},
		// M3U8 字幕
		{constant.Reg_ProxySubtitle, playlists.ProxySubtitle},

		// 资源下载, 重定向到直链
		{constant.Reg_ItemDownload, session.Redirect2OpenlistLink},
		{constant.Reg_ItemSyncDownload, session.HandleSyncDownload},

		// 处理图片请求
		{constant.Reg_Images, session.HandleImages},

		// Web CORS 处理
		{constant.Reg_VideoModWebDefined, session.ChangeBaseVideoModuleCorsDefined},
		{"forgotpassword.html", func(c *gin.Context) {
			// 返回 404
			c.Status(404)
			c.Writer.WriteString("404 Not Found")
		}},
		// 代理首页, 注入自定义脚本
		{constant.Reg_IndexHtml, session.ProxyIndexHtml},
		// 响应自定义脚本
		{constant.Route_CustomJs, session.ProxyCustomJs},
		// 响应自定义样式
		{constant.Route_CustomCss, session.ProxyCustomCss},

		// 根路径重定向到首页
		{constant.Reg_Root, session.ProxyRoot},

		// 其余资源走重定向回源
		{constant.Reg_All, session.ProxyOrigin},
	})
}
