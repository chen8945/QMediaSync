package models

import (
	"context"
	"errors"
	"sync"

	"gorm.io/gorm"
)

var syncPositions = struct {
	sync.Mutex
	version uint64
	writers int
	changed chan struct{}
	entries map[*gorm.Config]map[uint]syncPositionEntry
}{entries: make(map[*gorm.Config]map[uint]syncPositionEntry)}

type syncPositionEntry struct {
	version uint64
	files   []SyncFile
}

// BeginSyncPositionMutation 在位置写入前失效缓存，返回的结束函数必须在事务退出后、释放范围前调用。
// 即使事务部分提交或返回错误，也不能发布写入期间建立的快照。
func BeginSyncPositionMutation() func() {
	syncPositions.Lock()
	syncPositions.version++
	if syncPositions.writers == 0 {
		syncPositions.changed = make(chan struct{})
	}
	syncPositions.writers++
	syncPositions.entries = make(map[*gorm.Config]map[uint]syncPositionEntry)
	syncPositions.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			syncPositions.Lock()
			syncPositions.version++
			syncPositions.writers--
			if syncPositions.writers == 0 {
				close(syncPositions.changed)
				syncPositions.changed = nil
			}
			syncPositions.Unlock()
		})
	}
}

func syncPositionVersion() uint64 {
	syncPositions.Lock()
	defer syncPositions.Unlock()
	return syncPositions.version
}

func readSyncLogicalPositions(ctx context.Context, handle *gorm.DB, id uint, allFiles bool, fileID, pickCode string) ([]SyncFile, error) {
	for {
		syncPositions.Lock()
		if syncPositions.writers > 0 {
			changed := syncPositions.changed
			syncPositions.Unlock()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-changed:
				continue
			}
		}
		version := syncPositions.version
		entry, ok := syncPositions.entries[handle.Config][id]
		syncPositions.Unlock()
		if allFiles && ok && entry.version == version {
			return entry.files, nil
		}
		query := handle.WithContext(ctx).Model(&SyncFile{}).Select("id", "source_type", "account_id", "path", "local_file_path").Where("sync_path_id = ?", id).Order("id")
		if !allFiles {
			switch {
			case fileID != "" && pickCode != "":
				query = query.Where("(file_id = ? OR pick_code = ?)", fileID, pickCode)
			case fileID != "":
				query = query.Where("file_id = ?", fileID)
			case pickCode != "":
				query = query.Where("pick_code = ?", pickCode)
			default:
				return nil, nil
			}
		}
		query = query.Session(&gorm.Session{})
		var files []SyncFile
		var after uint
		for {
			var page []SyncFile
			if err := query.Where("id > ?", after).Limit(256).Find(&page).Error; err != nil {
				return nil, err
			}
			files = append(files, page...)
			if len(page) < 256 {
				break
			}
			after = page[len(page)-1].ID
		}
		syncPositions.Lock()
		stable := syncPositions.version == version && syncPositions.writers == 0
		if stable && allFiles {
			if syncPositions.entries[handle.Config] == nil {
				syncPositions.entries[handle.Config] = make(map[uint]syncPositionEntry)
			}
			syncPositions.entries[handle.Config][id] = syncPositionEntry{version, files}
		}
		syncPositions.Unlock()
		if stable {
			return files, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
}

// SameSyncFilePosition 只比较范围发现依赖的位置和归属，忽略内容、时间和签名。
func SameSyncFilePosition(a, b *SyncFile) bool {
	return a != nil && b != nil && a.SyncPathId == b.SyncPathId && a.SourceType == b.SourceType && a.AccountId == b.AccountId && a.Path == b.Path && a.LocalFilePath == b.LocalFilePath
}

// SaveSyncFilePosition 保存单文件或元数据记录，同时维护逻辑位置版本。
func SaveSyncFilePosition(handle *gorm.DB, file *SyncFile) error {
	var old SyncFile
	changed := file.ID == 0
	if !changed {
		err := handle.First(&old, file.ID).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		changed = err != nil || !SameSyncFilePosition(&old, file)
	}
	if changed {
		finish := BeginSyncPositionMutation()
		defer finish()
	}
	return handle.Save(file).Error
}
