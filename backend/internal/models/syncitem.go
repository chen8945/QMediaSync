package models

import (
	"qmediasync/internal/db"
	"qmediasync/internal/v115open"
)

type SyncTreeItemMetaAction int

const (
	SyncTreeItemMetaActionKeep   SyncTreeItemMetaAction = iota // 保留元数据
	SyncTreeItemMetaActionUpload                               // 上传元数据
	SyncTreeItemMetaActionDelete                               // 删除元数据
)

type SyncFile struct {
	BaseModel
	SourceType       SourceType        `json:"source_type"`
	AccountId        uint              `json:"account_id"`
	SyncPathId       uint              `json:"sync_path_id" gorm:"index:sync_path_id"`
	FileId           string            `json:"file_id" gorm:"index:file_id"`
	ParentId         string            `json:"parent_id"`
	FileName         string            `json:"file_name"`
	FileSize         int64             `json:"file_size"`
	FileType         v115open.FileType `json:"file_type"`
	PickCode         string            `json:"pick_code" gorm:"index:pick_code"`
	Sha1             string            `json:"sha1"`
	MTime            int64             `json:"mtime"`                                        // 最后修改时间
	LocalFilePath    string            `json:"local_file_path" gorm:"index:local_file_path"` // 本地文件路径，包含文件名
	Path             string            `json:"path"`                                         // 绝对路径，不包含 FileName
	SyncPath         *SyncPath         `json:"-" gorm:"-"`                                   // 关联的同步路径
	Sync             *Sync             `json:"-" gorm:"-"`                                   // 关联的同步项
	Account          *Account          `json:"-" gorm:"-"`                                   // 关联的账号
	IsVideo          bool              `json:"is_video"`
	IsMeta           bool              `json:"is_meta"`
	OpenlistSign     string            `json:"openlist_sign"` // OpenList 会返回 sign，用于生成 OpenList 文件链接
	OpenlistObjectId string            `json:"-"`             // OpenList 可选对象 ID，不改变 FileId 的路径语义
	OpenlistSHA1     string            `json:"-"`             // OpenList hash_info 明确返回的 SHA1
	OpenlistMD5      string            `json:"-"`             // OpenList hash_info 明确返回的 MD5
	Uploaded         bool              `json:"uploaded"`      // 是否上传完成，未上传完成的记录不触发删除
	ThumbUrl         string            `json:"thumb_url"`     // 缩略图 URL
	Processed        bool              `json:"processed"`     // 是否已处理
}

func (sf *SyncFile) GetAccount() *Account {
	if sf.Account == nil {
		sf.Account, _ = GetAccountById(sf.SyncPath.AccountId)
	}
	return sf.Account
}

func (sf *SyncFile) Save() error {
	return db.Db.Save(sf).Error
}

func GetSyncFileById(id uint) *SyncFile {
	if id == 0 {
		return nil
	}
	var db115File *SyncFile
	err := db.Db.Model(&SyncFile{}).Where("id = ?", id).First(&db115File).Error
	if err != nil {
		return nil
	}
	return db115File
}

func GetFileByPickCode(pickCode string) *SyncFile {
	if pickCode == "" {
		return nil
	}
	var db115File *SyncFile
	err := db.Db.Model(&SyncFile{}).Where("pick_code = ?", pickCode).First(&db115File).Error
	if err != nil {
		return nil
	}
	return db115File
}

func GetFilesBySyncPathId(syncPathId uint, offset, limit int) ([]*SyncFile, error) {
	var syncFiles []*SyncFile
	err := db.Db.Model(&SyncFile{}).Where("sync_path_id = ?", syncPathId).Offset(offset).Limit(limit).Find(&syncFiles).Error
	if err != nil {
		return nil, err
	}
	return syncFiles, nil
}
