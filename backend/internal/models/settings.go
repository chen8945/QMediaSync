package models

import (
	"encoding/json"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/notificationmanager"
)

var V115Login bool

const (
	DefaultURLValidityCheckEnabled        = 1
	DefaultURLValidityCheckTimeoutSeconds = 3
	MaxURLValidityCheckTimeoutSeconds     = 9
)

type SettingThreads struct {
	DownloadThreads    int `form:"download_threads" json:"download_threads" binding:"required" gorm:"default:1"`          // 下载 QPS
	FileDetailThreads  int `form:"file_detail_threads" json:"file_detail_threads" binding:"required" gorm:"default:1"`    // 115 接口 QPS
	OpenlistQPS        int `form:"openlist_qps" json:"openlist_qps" binding:"required" gorm:"default:3"`                  // OpenList QPS
	OpenlistRetry      int `form:"openlist_retry" json:"openlist_retry" binding:"required" gorm:"default:1"`              // OpenList 重试次数
	OpenlistRetryDelay int `form:"openlist_retry_delay" json:"openlist_retry_delay" binding:"required" gorm:"default:60"` // OpenList 重试间隔，单位：秒
	FileListPageSize   int `form:"file_list_page_size" json:"file_list_page_size" gorm:"default:1150"`                    // 115 文件列表每页查询数量，范围 100-1150
}

type SettingStrm struct {
	LocalProxy     int      `form:"local_proxy" json:"local_proxy" gorm:"default:0"`           // 是否使用本地代理，-1 表示使用 STRM 设置，0 表示不使用，1 表示使用
	StrmBaseUrl    string   `form:"strm_base_url" json:"strm_base_url"`                        // STRM 的基础 URL，用于生成网盘的流媒体播放地址
	Cron           string   `form:"cron" json:"cron"`                                          // 定时任务表达式
	MinVideoSize   int64    `form:"min_video_size" json:"min_video_size"`                      // 最小视频大小，单位字节，-1 表示使用 STRM 设置
	VideoExt       string   `json:"-"`                                                         // 视频文件扩展名，JSON 格式
	VideoExtArr    []string `json:"video_ext_arr" gorm:"-"`                                    // 视频文件扩展名数组，不参与数据库操作，仅供前端使用
	MetaExt        string   `json:"-"`                                                         // 元数据文件扩展名，JSON 格式
	MetaExtArr     []string `form:"meta_ext_arr" json:"meta_ext_arr" gorm:"-"`                 // 元数据文件扩展名数组，不参与数据库操作，仅供前端使用
	ExcludeName    string   `json:"-"`                                                         // 排除的文件名，JSON 格式
	ExcludeNameArr []string `form:"exclude_name_arr" json:"exclude_name_arr" gorm:"-"`         // 排除的文件名数组，不参与数据库操作，仅供前端使用
	UploadMeta     int      `form:"upload_meta" json:"upload_meta" gorm:"default:0"`           // 是否上传元数据，-1 表示使用 STRM 设置，0 表示保留，1 表示上传，2 表示删除
	DownloadMeta   int      `form:"download_meta" json:"download_meta" gorm:"default:0"`       // 是否下载元数据，-1 表示使用 STRM 设置，0 表示不下载，1 表示下载
	DeleteDir      int      `form:"delete_dir" json:"delete_dir" gorm:"default: 1"`            // 是否删除目录，-1 表示使用 STRM 设置，0 表示不删除，1 表示删除
	AddPath        int      `form:"add_path" json:"add_path" gorm:"default: 3"`                // STRM 链接路径模式，-1 表示使用 STRM 设置，1 表示添加完整路径，2 表示只添加文件名，3 表示不添加路径
	CheckMetaMtime int      `form:"check_meta_mtime" json:"check_meta_mtime" gorm:"default:0"` // 是否检查元数据文件修改时间，默认 -1（使用 Settings 的值），0 表示不检查，1 表示检查
}

type SettingUploadRapidWait struct {
	UploadRapidWaitEnabled         int   `form:"upload_rapid_wait_enabled" json:"upload_rapid_wait_enabled" gorm:"default:0"`                    // 是否启用秒传等待
	UploadRapidWaitTimeoutSeconds  int   `form:"upload_rapid_wait_timeout_seconds" json:"upload_rapid_wait_timeout_seconds" gorm:"default:0"`    // 秒传等待最大时长，单位秒
	UploadRapidWaitIntervalSeconds int   `form:"upload_rapid_wait_interval_seconds" json:"upload_rapid_wait_interval_seconds" gorm:"default:60"` // 秒传等待重试间隔，单位秒
	UploadRapidWaitMinSize         int64 `form:"upload_rapid_wait_min_size" json:"upload_rapid_wait_min_size" gorm:"default:0"`                  // 启用秒传等待的最小文件大小
	UploadRapidWaitForceSize       int64 `form:"upload_rapid_wait_force_size" json:"upload_rapid_wait_force_size" gorm:"default:0"`              // 强制等待到超时的文件大小阈值
	UploadRapidWaitSkipUpload      int   `form:"upload_rapid_wait_skip_upload" json:"upload_rapid_wait_skip_upload" gorm:"default:0"`            // 等待超时后是否跳过真实上传
}

type SettingURLValidityCheck struct {
	URLValidityCheckEnabled        int `form:"url_validity_check_enabled" json:"url_validity_check_enabled" gorm:"default:1"`                 // 是否启用 115 直链缓存有效性检查
	URLValidityCheckTimeoutSeconds int `form:"url_validity_check_timeout_seconds" json:"url_validity_check_timeout_seconds" gorm:"default:3"` // 115 直链缓存有效性检查总超时，单位秒
}

type SettingThreadAndRapidWait struct {
	SettingThreads
	SettingUploadRapidWait
	SettingURLValidityCheck
}

type Settings struct {
	BaseModel
	SettingThreads
	SettingStrm
	SettingUploadRapidWait
	SettingURLValidityCheck
	UseTelegram      int8   `json:"use_telegram"`       // @deprecated 已迁移到 TelegramChannelConfig 是否使用 Telegram Bot 通知
	TelegramBotToken string `json:"telegram_bot_token"` // @deprecated 已迁移到 TelegramChannelConfig Telegram Bot Token
	TelegramChatId   string `json:"telegram_chat_id"`   // @deprecated 已迁移到 TelegramChannelConfig Telegram Chat ID
	MeoWName         string `json:"meow_name"`          // @deprecated 已迁移到 MeoWChannelConfig MeoW 昵称，用于发送 MeoW 消息
	EmbyUrl          string `json:"emby_url"`           // @deprecated 已迁移到 EmbyConfig Emby 的主机地址
	EmbyApiKey       string `json:"emby_api_key"`       // @deprecated 已迁移到 EmbyConfig Emby 的 API Key
	HttpProxy        string `json:"http_proxy"`         // 出站代理地址，支持 http、https、socks5 和 socks5h
	// LocalProxy       int    `json:"local_proxy" gorm:"default:0"` // 是否启用本地代理，0 表示不启用，1 表示启用
}

func (t SettingThreads) ToMap() map[string]any {
	return map[string]any{
		"download_threads":     t.DownloadThreads,
		"file_detail_threads":  t.FileDetailThreads,
		"openlist_qps":         t.OpenlistQPS,
		"openlist_retry":       t.OpenlistRetry,
		"openlist_retry_delay": t.OpenlistRetryDelay,
		"file_list_page_size":  t.FileListPageSize,
	}
}

func (s SettingStrm) ToMap(isDb bool, isSetting bool) map[string]any {
	// helpers.AppLogger.Debugf("SettingStrm: %+v", s)
	dataMap := map[string]any{
		"cron":             s.Cron,
		"min_video_size":   s.MinVideoSize,
		"delete_dir":       s.DeleteDir,
		"upload_meta":      s.UploadMeta,
		"download_meta":    s.DownloadMeta,
		"strm_base_url":    s.StrmBaseUrl,
		"add_path":         s.AddPath,
		"check_meta_mtime": s.CheckMetaMtime,
		"local_proxy":      s.LocalProxy,
	}
	if s.Cron == "" && isSetting {
		dataMap["cron"] = helpers.GlobalConfig.Strm.Cron // 使用默认配置
	} else {
		dataMap["cron"] = s.Cron
	}
	if before, ok := strings.CutSuffix(dataMap["strm_base_url"].(string), "/"); ok {
		dataMap["strm_base_url"] = before
	}
	if !isDb {
		// 不是数据库则返回数组
		if s.MetaExt != "" {
			dataMap["meta_ext_arr"] = s.MetaExtArr
		} else if isSetting {
			// 从配置文件中读取默认的 metaExt
			dataMap["meta_ext_arr"] = helpers.GlobalConfig.Strm.MetaExt
		}
		if s.VideoExt != "" {
			dataMap["video_ext_arr"] = s.VideoExtArr
		} else if isSetting {
			// 从配置文件中读取默认的视频扩展名
			dataMap["video_ext_arr"] = helpers.GlobalConfig.Strm.VideoExt
		}
		if s.ExcludeName != "" {
			dataMap["exclude_name_arr"] = s.ExcludeNameArr
		} else {
			// 从配置文件中读取默认的排除的文件名
			dataMap["exclude_name_arr"] = []string{}
		}
	} else {
		// 数据库
		dataMap["exclude_name"] = s.ExcludeName
		dataMap["meta_ext"] = s.MetaExt
		dataMap["video_ext"] = s.VideoExt
	}
	return dataMap
}

func (s SettingUploadRapidWait) ToMap() map[string]any {
	return map[string]any{
		"upload_rapid_wait_enabled":          s.UploadRapidWaitEnabled,
		"upload_rapid_wait_timeout_seconds":  s.UploadRapidWaitTimeoutSeconds,
		"upload_rapid_wait_interval_seconds": s.UploadRapidWaitIntervalSeconds,
		"upload_rapid_wait_min_size":         s.UploadRapidWaitMinSize,
		"upload_rapid_wait_force_size":       s.UploadRapidWaitForceSize,
		"upload_rapid_wait_skip_upload":      s.UploadRapidWaitSkipUpload,
	}
}

func (s SettingURLValidityCheck) ToMap() map[string]any {
	return map[string]any{
		"url_validity_check_enabled":         s.URLValidityCheckEnabled,
		"url_validity_check_timeout_seconds": s.URLValidityCheckTimeoutSeconds,
	}
}

func (s SettingThreadAndRapidWait) ToMap() map[string]any {
	dataMap := s.SettingThreads.ToMap()
	for key, value := range s.SettingUploadRapidWait.ToMap() {
		dataMap[key] = value
	}
	for key, value := range s.SettingURLValidityCheck.ToMap() {
		dataMap[key] = value
	}
	return dataMap
}

func (s SettingStrm) EncodeArr() *SettingStrm {
	// 全部转小写
	for i, v := range s.MetaExtArr {
		s.MetaExtArr[i] = strings.ToLower(v)
	}
	// 全部转小写
	for i, v := range s.VideoExtArr {
		s.VideoExtArr[i] = strings.ToLower(v)
	}
	// 全部转小写
	for i, v := range s.ExcludeNameArr {
		s.ExcludeNameArr[i] = strings.ToLower(v)
	}
	metaExtStr, err := json.Marshal(s.MetaExtArr)
	if err != nil {
		helpers.AppLogger.Errorf("将元数据扩展名转换为 JSON 字符串失败：%v", err)
		return nil
	}
	videoExtStr, err := json.Marshal(s.VideoExtArr)
	if err != nil {
		helpers.AppLogger.Errorf("将视频扩展名转换为 JSON 字符串失败：%v", err)
		return nil
	}
	// 排除的名字
	excludeNameStr, err := json.Marshal(s.ExcludeNameArr)
	if err != nil {
		helpers.AppLogger.Errorf("将排除的名字转换为 JSON 字符串失败：%v", err)
		return nil
	}
	s.ExcludeName = string(excludeNameStr)
	s.VideoExt = string(videoExtStr)
	s.MetaExt = string(metaExtStr)
	return &s
}

func (s SettingStrm) DecodeArr(isSetting bool) *SettingStrm {
	if s.MetaExt != "" {
		if err := json.Unmarshal([]byte(s.MetaExt), &s.MetaExtArr); err != nil {
			helpers.AppLogger.Errorf("将元数据扩展名转换为数组失败：%v", err)
			return nil
		}
	}
	if len(s.MetaExtArr) == 0 && isSetting {
		s.MetaExtArr = helpers.GlobalConfig.Strm.MetaExt
	}
	if s.VideoExt != "" {
		if err := json.Unmarshal([]byte(s.VideoExt), &s.VideoExtArr); err != nil {
			helpers.AppLogger.Errorf("将视频扩展名转换为数组失败：%v", err)
			return nil
		}
	}
	if len(s.VideoExtArr) == 0 && isSetting {
		s.VideoExtArr = helpers.GlobalConfig.Strm.VideoExt
	}
	if s.ExcludeName != "" {
		if err := json.Unmarshal([]byte(s.ExcludeName), &s.ExcludeNameArr); err != nil {
			helpers.AppLogger.Errorf("将排除的名字转换为数组失败：%v", err)
			return nil
		}
	}
	if len(s.ExcludeNameArr) == 0 {
		s.ExcludeNameArr = []string{}
	}
	if s.Cron == "" && isSetting {
		s.Cron = helpers.GlobalConfig.Strm.Cron
	}
	return &s
}

var SettingsGlobal = &Settings{}

func (settings *Settings) ThreadAndRapidWait() SettingThreadAndRapidWait {
	return SettingThreadAndRapidWait{
		SettingThreads:          settings.SettingThreads,
		SettingUploadRapidWait:  settings.SettingUploadRapidWait,
		SettingURLValidityCheck: settings.SettingURLValidityCheck,
	}
}

func (settings *Settings) UpdateThreads(req SettingThreadAndRapidWait) bool {
	settings.SettingThreads = req.SettingThreads
	settings.SettingUploadRapidWait = req.SettingUploadRapidWait
	settings.SettingURLValidityCheck = req.SettingURLValidityCheck

	updateData := req.ToMap()

	err := db.Db.Model(settings).Where("id = ?", settings.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新线程数失败：%v", err)
		return false
	}
	// 重新初始化下载队列
	InitDQ()
	return true
}

// func (settings *Settings) UpdateTelegramBot(enabled bool, token string, chatId string) bool {
// 	if enabled {
// 		settings.UseTelegram = 1
// 	} else {
// 		settings.UseTelegram = 0
// 	}
// 	settings.TelegramBotToken = token
// 	settings.TelegramChatId = chatId
// 	updateData := make(map[string]any)
// 	updateData["use_telegram"] = settings.UseTelegram
// 	updateData["telegram_bot_token"] = token
// 	updateData["telegram_chat_id"] = chatId
// 	err := db.Db.Model(settings).Where("id = ?", settings.ID).Updates(updateData).Error
// 	if err != nil {
// 		helpers.AppLogger.Errorf("更新 Telegram 通知设置失败：%v", err)
// 		return false
// 	}
// 	InitNotificationManager()
// 	return true
// }

func (settings *Settings) UpdateHttpProxy(httpProxy string) bool {
	settings.HttpProxy = httpProxy
	updateData := make(map[string]interface{})
	updateData["http_proxy"] = httpProxy
	err := db.Db.Model(settings).Where("id = ?", settings.ID).Updates(updateData).Error
	if err != nil {
		helpers.AppLogger.Errorf("更新 HTTP 代理失败：%v", err)
		return false
	}
	InitNotificationManager()
	return true
}

func (settings *Settings) UpdateStrm(req SettingStrm) bool {
	strm := req.EncodeArr()
	if strm == nil {
		helpers.AppLogger.Errorf("编码 STRM 设置失败")
		return false
	}
	settings.SettingStrm = *strm

	// ctx := context.Background()
	updateData := strm.ToMap(true, true)
	// helpers.AppLogger.Infof("更新 STRM 设置：%+v", updateData)
	err := db.Db.Model(settings).Where("id = ?", settings.ID).Updates(updateData).Error
	// _, err = gorm.G[Settings](db.Db).Where("id = ?", settings.ID).Updates(ctx, updateData)
	if err != nil {
		helpers.AppLogger.Errorf("更新 STRM 设置失败：%v", err)
		return false
	}
	return true
}

func LoadSettings() {
	if err := db.Db.Take(SettingsGlobal).Error; err != nil {
		helpers.AppLogger.Errorf("load settings failed: %v", err)
		return
	}
	SettingsGlobal.SettingStrm = *SettingsGlobal.SettingStrm.DecodeArr(true)
	if SettingsGlobal.MinVideoSize == 104857600 {
		SettingsGlobal.MinVideoSize = 100
		db.Db.Save(SettingsGlobal)
	}
}

func InitNotificationManager() {
	// 初始化增强通知管理器
	// 传入代理获取回调函数，避免循环依赖
	enhancedManager := notificationmanager.NewEnhancedNotificationManager(db.Db, func() string {
		helpers.AppLogger.Infof("获取 HTTP 代理：%+v", SettingsGlobal.HttpProxy)
		if SettingsGlobal != nil {
			return SettingsGlobal.HttpProxy
		}
		return ""
	})
	if err := enhancedManager.LoadChannels(); err != nil {
		helpers.AppLogger.Warnf("加载通知渠道失败：%v", err)
	}
	notificationmanager.GlobalEnhancedNotificationManager = enhancedManager
}

// GetFileListPageSize 获取 115 文件列表每页查询数量
// 如果配置不存在或不在范围内（100-1150），返回默认值 1150
func GetFileListPageSize() int {
	pageSize := SettingsGlobal.FileListPageSize
	if pageSize < 100 || pageSize > 1150 {
		return 1150
	}
	return pageSize
}

// IsURLValidityCheckEnabled 返回 115 直链缓存有效性检查是否启用。
func IsURLValidityCheckEnabled() bool {
	if SettingsGlobal == nil {
		return DefaultURLValidityCheckEnabled == 1
	}
	return SettingsGlobal.URLValidityCheckEnabled == 1
}

// URLValidityCheckTimeout 返回 115 直链缓存有效性检查总超时。
func URLValidityCheckTimeout() time.Duration {
	timeoutSeconds := DefaultURLValidityCheckTimeoutSeconds
	if SettingsGlobal != nil && SettingsGlobal.URLValidityCheckTimeoutSeconds >= 1 {
		timeoutSeconds = SettingsGlobal.URLValidityCheckTimeoutSeconds
		if timeoutSeconds > MaxURLValidityCheckTimeoutSeconds {
			timeoutSeconds = MaxURLValidityCheckTimeoutSeconds
		}
	}
	return time.Duration(timeoutSeconds) * time.Second
}
