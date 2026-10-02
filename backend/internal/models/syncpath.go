package models

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/syncscope"

	"gorm.io/gorm"
)

type SourceType string

const (
	SourceType115       SourceType = "115"
	SourceTypeLocal     SourceType = "local"
	SourceType123       SourceType = "123"
	SourceTypeOpenList  SourceType = "openlist"
	SourceTypeBaiduPan  SourceType = "baidupan"
	SourceTypeEmbyMedia SourceType = "emby_media" // Emby 媒体信息提取专用
)

func (s SourceType) String() string {
	switch s {
	case SourceType115:
		return "115"
	case SourceTypeLocal:
		return "本地"
	case SourceType123:
		return "123"
	case SourceTypeOpenList:
		return "OpenList"
	case SourceTypeBaiduPan:
		return "百度网盘"
	case SourceTypeEmbyMedia:
		return "Emby 媒体信息提取"
	default:
		return string(s)
	}
}

type SyncPath struct {
	ScopeLocalRoot string `gorm:"-" json:"-"`
	scopeHeld      []syncscope.Scope
	BaseModel
	SettingStrm
	CustomConfig           bool       `json:"custom_config"`                                 // 是否自定义配置
	BaseCid                string     `json:"base_cid" gorm:"unique"`                        // 同步源路径的目录 ID，115 网盘和 123 网盘需要该字段
	LocalPath              string     `json:"local_path"`                                    // 存放 STRM 文件和元数据文件的本地路径
	RemotePath             string     `json:"remote_path"`                                   // 同步源路径
	SourceType             SourceType `json:"source_type"`                                   // 同步源类型，主要分为 115 网盘、本地目录和 123 网盘，无法编辑
	AccountId              uint       `json:"account_id"`                                    // 115 账号 ID 或 123 账号 ID，根据 SourceType 决定，无法编辑
	EnableCron             bool       `json:"enable_cron"`                                   // 是否启用定时同步
	DirectoryUploadEnabled bool       `json:"directory_upload_enabled" gorm:"default:false"` // 是否启用目录监控上传总开关
	LastSyncAt             int64      `json:"last_sync_at"`                                  // 上次同步时间
	AccountName            string     `json:"account_name" gorm:"-"`                         // 115 账号名或 123 账号名，不参与数据库操作，仅供前端使用
	IsFullSync             bool       `json:"is_full_sync"`                                  // 是否全量同步，默认 false
	IsRunning              int        `json:"is_running" gorm:"-"`                           // 是否正在运行：0 未运行，1 已在队列，2 正在运行
}

// SyncPathWriteInput 描述同步目录事务写入字段。
type SyncPathWriteInput struct {
	SourceType             SourceType
	AccountID              uint
	BaseCid                string
	LocalPath              string
	RemotePath             string
	EnableCron             bool
	DirectoryUploadEnabled bool
	CustomConfig           bool
	Setting                SettingStrm
}

type SyncPathScrapePath struct {
	BaseModel
	SyncPathId   uint `json:"sync_path_id" form:"sync_path_id"  gorm:"uniqueIndex:sync_path_id_scrape_path_id"`    // 同步目录 ID
	ScrapePathId uint `json:"scrape_path_id" form:"scrape_path_id" gorm:"uniqueIndex:sync_path_id_scrape_path_id"` // 刮削路径 ID
}

func GetStrmSettingDefault() SettingStrm {
	return SettingStrm{
		StrmBaseUrl:         "",
		Cron:                "",
		MinVideoSize:        -1,
		AddPath:             -1,
		CheckMetaMtime:      -1,
		UploadMeta:          -1,
		DownloadMeta:        -1,
		DeleteDir:           -1,
		VideoExtArr:         []string{},
		MetaExtArr:          []string{},
		ExcludeNameArr:      []string{},
		ExcludeNameRegexArr: []string{},
		VideoExt:            "",
		MetaExt:             "",
		ExcludeName:         "",
		ExcludeNameRegex:    "[]",
	}
}

func (sp *SyncPath) GetScrapePathIds() []uint {
	var scrapePathIds []uint
	db.Db.Model(&SyncPathScrapePath{}).Where("sync_path_id = ?", sp.ID).Pluck("scrape_path_id", &scrapePathIds)
	return scrapePathIds
}

func (sp *SyncPath) SaveScrapePaths(scrapePathIds []uint) error {
	tx := db.Db.Begin()
	// 删除旧关联
	if err := tx.Where("sync_path_id = ?", sp.ID).Delete(&SyncPathScrapePath{}).Error; err != nil {
		tx.Rollback()
		return err
	}
	if len(scrapePathIds) == 0 {
		tx.Commit()
		return nil
	}
	// 保存关联的刮削路径
	var syncPathScrapePaths []*SyncPathScrapePath
	for _, id := range scrapePathIds {
		syncPathScrapePaths = append(syncPathScrapePaths, &SyncPathScrapePath{
			SyncPathId:   sp.ID,
			ScrapePathId: id,
		})
	}
	if err := tx.Save(&syncPathScrapePaths).Error; err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit().Error
}

func (sp *SyncPath) GetUploadMeta() int {
	if sp.UploadMeta == -1 {
		return SettingsGlobal.UploadMeta
	}
	return sp.UploadMeta
}

func (sp *SyncPath) GetDownloadMeta() int {
	if sp.DownloadMeta == -1 {
		return SettingsGlobal.DownloadMeta
	}
	return sp.DownloadMeta
}

func (sp *SyncPath) GetDeleteDir() int {
	if sp.DeleteDir == -1 {
		return SettingsGlobal.DeleteDir
	}
	return sp.DeleteDir
}

func (sp *SyncPath) GetMinVideoSize() int64 {
	if sp.MinVideoSize == -1 {
		return SettingsGlobal.MinVideoSize
	}
	return sp.MinVideoSize
}

func (sp *SyncPath) GetVideoExt() []string {
	if len(sp.VideoExtArr) == 0 {
		return SettingsGlobal.VideoExtArr
	}
	return sp.VideoExtArr
}

func (sp *SyncPath) GetMetaExt() []string {
	if len(sp.MetaExtArr) == 0 {
		return SettingsGlobal.MetaExtArr
	}
	return sp.MetaExtArr
}

func (sp *SyncPath) GetExcludeNameArr() []string {
	if len(sp.ExcludeNameArr) == 0 {
		return SettingsGlobal.ExcludeNameArr
	}
	return sp.ExcludeNameArr
}

// GetExcludeNameRegexArr 返回自定义正则排除名称，空列表继承全局设置。
func (sp *SyncPath) GetExcludeNameRegexArr() []string {
	if len(sp.ExcludeNameRegexArr) == 0 {
		return SettingsGlobal.ExcludeNameRegexArr
	}
	return sp.ExcludeNameRegexArr
}

func (sp *SyncPath) GetAddPath() int {
	if sp.AddPath != -1 {
		return sp.AddPath
	}
	return SettingsGlobal.AddPath
}

func (sp *SyncPath) GetCheckMetaMtime() int {
	if sp.CheckMetaMtime == -1 {
		return SettingsGlobal.CheckMetaMtime
	}
	return sp.CheckMetaMtime
}

func (sp *SyncPath) GetCron() string {
	if sp.Cron == "" {
		return SettingsGlobal.Cron
	}
	return sp.Cron
}

func (sp *SyncPath) GetStrmBaseUrl() string {
	if sp.StrmBaseUrl == "" {
		return SettingsGlobal.StrmBaseUrl
	}
	return sp.StrmBaseUrl
}

// CreateSyncPathWithDB 在指定事务中创建同步目录并通过实体获得回填 ID。
// 调用方须在开启事务前申请同步范围。
func CreateSyncPathWithDB(tx *gorm.DB, input SyncPathWriteInput) (*SyncPath, error) {
	if tx == nil {
		return nil, errors.New("数据库连接为空")
	}
	syncPath := &SyncPath{}
	if err := applySyncPathWriteInput(syncPath, input); err != nil {
		return nil, err
	}
	if err := tx.Select("*").Create(syncPath).Error; err != nil {
		return nil, err
	}
	return syncPath, nil
}

// UpdateSyncPathWithDB 在指定事务中更新同步目录。
// 调用方须在开启事务前申请旧、新范围，并在等待后重新读取目录。
func UpdateSyncPathWithDB(tx *gorm.DB, syncPath *SyncPath, input SyncPathWriteInput) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	if syncPath == nil || syncPath.ID == 0 {
		return errors.New("同步目录为空")
	}
	if err := applySyncPathWriteInput(syncPath, input); err != nil {
		return err
	}
	return tx.Select("*").Save(syncPath).Error
}

func applySyncPathWriteInput(syncPath *SyncPath, input SyncPathWriteInput) error {
	if syncPath == nil {
		return errors.New("同步目录为空")
	}
	localPath := input.LocalPath
	remotePath := input.RemotePath
	if runtime.GOOS != "windows" {
		localPath = strings.TrimRight(localPath, "/")
		remotePath = strings.Trim(remotePath, "/")
	} else {
		localPath = strings.TrimRight(localPath, "\\")
		remotePath = strings.TrimRight(remotePath, "\\")
	}
	setting := input.Setting
	if input.CustomConfig {
		encoded := setting.EncodeArr()
		if encoded == nil {
			return errors.New("将同步路径设置编码为 JSON 字符串失败")
		}
		setting = *encoded
	} else {
		setting = GetStrmSettingDefault()
	}
	syncPath.SourceType = input.SourceType
	syncPath.AccountId = input.AccountID
	syncPath.BaseCid = input.BaseCid
	syncPath.LocalPath = localPath
	syncPath.RemotePath = remotePath
	syncPath.EnableCron = input.EnableCron
	syncPath.DirectoryUploadEnabled = input.DirectoryUploadEnabled
	syncPath.CustomConfig = input.CustomConfig
	syncPath.SettingStrm = setting
	return nil
}

func (sp *SyncPath) SetIsFullSync(isFullSync bool) error {
	if err := updateSyncPathField(sp.ID, "is_full_sync", isFullSync); err != nil {
		return err
	}
	sp.IsFullSync = isFullSync
	return nil
}

// 给同步路径创建一个同步任务
func (sp *SyncPath) CreateSyncTask() *Sync {
	// 新建同步任务
	sync := &Sync{
		SyncPathId: sp.ID,
		Status:     SyncStatusPending,
		SubStatus:  SyncSubStatusNone,
		FileOffset: 0,
		Total:      0,
		NewStrm:    0,
		NewMeta:    0,
		Logger:     nil,
		LocalPath:  sp.LocalPath,
		RemotePath: sp.RemotePath,
		BaseCid:    sp.BaseCid,
		SyncPath:   sp,
		FailReason: "",
		IsFullSync: sp.IsFullSync,
	}
	// 写入数据库
	if err := db.Db.Save(sync).Error; err != nil {
		helpers.AppLogger.Errorf("创建同步任务失败：%v", err)
		return nil
	}
	// helpers.AppLogger.Debugf("创建同步任务：%d", sync.ID)
	sync.SyncPath = sp // 将 SyncPath 实例赋值给 sync
	return sync
}

// 获取完整的本地路径
func (sp *SyncPath) GetFullLocalPath() string {
	if sp.SourceType == SourceTypeLocal {
		return sp.LocalPath
	}
	return filepath.Join(sp.LocalPath, sp.RemotePath)
}

func (sp *SyncPath) ParseVideoAndMetaExt() {
	sp.SettingStrm = *sp.SettingStrm.DecodeArr(false)
}

func (sp *SyncPath) UpdateLastSync() error {
	finishedAt := time.Now().Unix()
	if err := updateSyncPathField(sp.ID, "last_sync_at", finishedAt); err != nil {
		return err
	}
	sp.LastSyncAt = finishedAt
	return nil
}

func (sp *SyncPath) ToggleCron() error {
	if err := updateSyncPathField(sp.ID, "enable_cron", !sp.EnableCron); err != nil {
		return err
	}
	sp.EnableCron = !sp.EnableCron
	return nil
}

func updateSyncPathField(id uint, column string, value any) error {
	result := db.Db.Model(&SyncPath{}).Where("id = ?", id).Update(column, value)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (sp *SyncPath) IsValidVideoExt(name string) bool {
	ext := filepath.Ext(name)
	ext = strings.ToLower(ext)
	if slices.Contains(sp.GetVideoExt(), ext) {
		return true
	}
	// return ext == ".strm"
	return false
}

func (sp *SyncPath) IsValidMetaExt(name string) bool {
	ext := filepath.Ext(name)
	ext = strings.ToLower(ext)
	return slices.Contains(sp.GetMetaExt(), ext)
}

func (sp *SyncPath) MakeFullLocalPath(pid, name string) string {
	if sp.IsValidVideoExt(name) {
		// 视频文件要转成 STRM 文件
		ext := filepath.Ext(name)
		baseName := strings.TrimSuffix(name, ext)
		// if ext == ".iso" {
		// 	name = name + ".strm"
		// } else {
		name = baseName + ".strm"
		// }
	}
	switch sp.SourceType {
	case SourceType115:
		return filepath.Join(sp.LocalPath, sp.RemotePath, pid, name)
	case SourceTypeOpenList:
		return filepath.Join(sp.LocalPath, pid, name)
	case SourceTypeLocal:
		return filepath.Join(sp.LocalPath, pid, name)
	}
	return ""
}

// DeleteSyncPathById 删除同步目录及其记录，保留已有调用方式。
func DeleteSyncPathById(id uint) bool {
	return DeleteSyncPathByID(context.Background(), id) == nil
}

// DeleteSyncPathByID 等待文件处理结束后删除同步目录，不删除本地文件。
func DeleteSyncPathByID(ctx context.Context, id uint) error {
	_, release, err := AcquireSyncPathConfigScope(ctx, db.Db, id)
	if err != nil {
		return err
	}
	defer release()
	finish := BeginSyncPositionMutation()
	defer finish()
	return db.Db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Delete(&SyncPath{}, id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return gorm.ErrRecordNotFound
		}
		for _, model := range []any{
			&SyncFile{}, &DirectoryUploadRule{}, &DirectoryUploadProcessedFile{},
			&EmbyLibrarySyncPath{}, &EmbyMediaSyncFile{},
		} {
			if err := tx.Where("sync_path_id = ?", id).Delete(model).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// 根据 ID 获取同步路径
func GetSyncPathById(id uint) *SyncPath {
	var syncPath SyncPath
	db.Db.First(&syncPath, id)
	if syncPath.ID == 0 {
		helpers.AppLogger.Errorf("同步路径不存在：%v", id)
		return nil
	}
	syncPath.ParseVideoAndMetaExt()
	return &syncPath
}

// 查询同步路径列表
func GetSyncPathList(page, pageSize int, enableCron bool, sourceType SourceType) ([]*SyncPath, int64) {
	var syncPaths []*SyncPath
	var total int64

	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	offset := (page - 1) * pageSize
	query := db.Db.Model(&SyncPath{})
	if enableCron {
		query.Where("enable_cron = ?", enableCron)
	}
	if sourceType != "" {
		query.Where("source_type = ?", sourceType)
	}
	query.Count(&total)
	query.Offset(offset).Limit(pageSize).Order("id DESC").Find(&syncPaths)
	accountCache := make(map[uint]*Account)
	for _, syncPath := range syncPaths {
		// syncPath.ParseVideoAndMetaExt()
		if syncPath.AccountId == 0 {
			continue
		}
		if account, ok := accountCache[syncPath.AccountId]; ok {
			syncPath.AccountName = account.Name
			// helpers.AppLogger.Infof("从缓存获取账号成功：%s", account.Name)
			continue
		}
		account, err := GetAccountById(syncPath.AccountId)
		if err != nil {
			helpers.AppLogger.Errorf("获取账号失败：%v", err)
			continue
		}
		accountCache[syncPath.AccountId] = account
		syncPath.AccountName = account.Name
		if account.Name == "" {
			syncPath.AccountName = account.Username
		}
		syncPath.ParseVideoAndMetaExt()
		// helpers.AppLogger.Infof("获取账号成功：%s", account.Name)
	}
	// // 清空 accountCache
	// accountCache = nil
	return syncPaths, total
}

// 根据账号 ID 获取同步路径列表
func GetAllSyncPathByAccountId(accountId uint) []SyncPath {
	var syncPaths []SyncPath
	db.Db.Where("account_id = ?", accountId).Find(&syncPaths)
	return syncPaths
}
