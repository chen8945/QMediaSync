package syncstrm

import (
	"fmt"
	"strings"

	"gorm.io/gorm"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

const syncLedgerPageSize = 256

var syncLedgerColumns = [...]string{
	"file_name", "pick_code", "file_size", "m_time", "path", "local_file_path",
	"thumb_url", "openlist_sign", "openlist_object_id", "openlist_sha1", "openlist_md5", "sha1", "parent_id",
}

func syncLedgerValues(file *models.SyncFile) [13]any {
	return [13]any{
		file.FileName, file.PickCode, file.FileSize, file.MTime, file.Path, file.LocalFilePath,
		file.ThumbUrl, file.OpenlistSign, file.OpenlistObjectId, file.OpenlistSHA1, file.OpenlistMD5, file.Sha1, file.ParentId,
	}
}

type syncLedgerRow struct {
	models.SyncFile
	LedgerNullFields bool
}

type syncLedgerUpdate struct {
	id     uint
	values [13]any
}

// handleTempTableDiff 只由持有目录范围的后台调用，扫描、下载入队和本地清理已结束。
// 每页提交成功后才消费缓存；失败页回滚，后续任务重新扫描，不重放旧缓存。
func (s *SyncStrm) handleTempTableDiff() error {
	if err := s.Context.Err(); err != nil {
		return err
	}
	ledgerDB := db.Db.WithContext(s.Context)
	// 父目录索引的最后一个读者已退出，删除缓存时无需再逐条搬移子项。
	s.memSyncCache.releaseLedgerParentIndex()
	s.Sync.Logger.Infof("内存同步缓存中共有 %d 条数据，开始分批保存", s.memSyncCache.Count())

	// 百度增量只替换本轮成功确认的同身份旧路径，未知缺项仍留待原定全量。
	successfulBaiduPaths := make(map[string]string)
	if s.Account.SourceType == models.SourceTypeBaiduPan && s.missingCleanupReason != "" {
		for _, file := range s.memSyncCache.GetAllFile() {
			if err := s.Context.Err(); err != nil {
				return err
			}
			s.scanMu.Lock()
			succeeded := s.scanFiles[file.GetFileId()] == "succeeded"
			s.scanMu.Unlock()
			if succeeded && file.PickCode != "" && file.PickCode != "0" && !s.ledgerProtected(file.GetSyncFile(s, s.Account.BaseUrl)) {
				successfulBaiduPaths[file.PickCode] = file.GetFileId()
			}
		}
	}
	var supersededIDs []uint
	var cursor uint
	for {
		var page []syncLedgerRow
		// 历史 NULL 与 Go 零值不同；原写法会写回零值，不能把它们当作无需更新。
		nullFields := "(" + strings.Join(syncLedgerColumns[:], " IS NULL OR ") + " IS NULL) AS ledger_null_fields"
		if err := ledgerDB.Model(&models.SyncFile{}).Select("*, "+nullFields).
			Where("sync_path_id = ? AND id > ?", s.SyncPathId, cursor).
			Order("id ASC").Limit(syncLedgerPageSize).Find(&page).Error; err != nil {
			return fmt.Errorf("读取同步文件记录失败：%w", err)
		}
		if len(page) == 0 {
			break
		}
		var unchanged, deleted []uint
		var changed []syncLedgerUpdate
		positionChanged := false
		consumed := make(map[string]bool, len(page))
		for _, row := range page {
			if err := s.Context.Err(); err != nil {
				return err
			}
			file := &row.SyncFile
			if s.ledgerProtected(file) {
				consumed[file.FileId] = true
				continue
			}
			cached, _ := s.memSyncCache.GetByFileId(file.FileId)
			if consumed[file.FileId] {
				// 与逐条处理相同：同一身份先匹配最早的 ID。
				cached = nil
			}
			if cached == nil {
				if s.missingCleanupReason != "" {
					if newPath := successfulBaiduPaths[file.PickCode]; newPath != "" && newPath != file.FileId {
						supersededIDs = append(supersededIDs, file.ID)
					}
				} else {
					deleted = append(deleted, file.ID)
				}
				continue
			}
			next := cached.GetSyncFile(s, s.Account.BaseUrl)
			consumed[file.FileId] = true
			if s.ledgerProtected(next) {
				// 新位置扫描不完整时，即使旧位置可清理也保留旧行。
				continue
			}
			positionChanged = positionChanged || !models.SameSyncFilePosition(file, next)
			values := syncLedgerValues(next)
			if !row.LedgerNullFields && values == syncLedgerValues(file) {
				unchanged = append(unchanged, file.ID)
			} else {
				changed = append(changed, syncLedgerUpdate{id: file.ID, values: values})
			}
		}
		if err := syncPositionTransaction(ledgerDB, positionChanged || len(deleted) > 0, func(tx *gorm.DB) error {
			updatedAt := tx.NowFunc().Unix()
			if len(unchanged) > 0 {
				if err := tx.Model(&models.SyncFile{}).Where("sync_path_id = ? AND id IN ?", s.SyncPathId, unchanged).
					UpdateColumn("updated_at", updatedAt).Error; err != nil {
					return err
				}
			}
			if err := updateSyncLedgerRows(tx, s.SyncPathId, changed, updatedAt); err != nil {
				return err
			}
			if len(deleted) > 0 {
				return tx.Where("sync_path_id = ? AND id IN ?", s.SyncPathId, deleted).Delete(&models.SyncFile{}).Error
			}
			return s.Context.Err()
		}); err != nil {
			return fmt.Errorf("保存同步文件记录失败：%w", err)
		}
		for id := range consumed {
			s.memSyncCache.DeleteByFileId(id)
		}
		cursor = page[len(page)-1].ID
		if len(page) < syncLedgerPageSize {
			break
		}
	}

	// 剩余缓存是新增记录；SQL 子批成功也要等整页提交后才移除。
	rows := make([]models.SyncFile, 0, syncLedgerPageSize)
	flush := func() error {
		if len(rows) == 0 {
			return nil
		}
		if err := syncPositionTransaction(ledgerDB, true, func(tx *gorm.DB) error {
			return tx.Session(&gorm.Session{SkipDefaultTransaction: true}).CreateInBatches(&rows, 32).Error
		}); err != nil {
			return fmt.Errorf("插入同步文件记录失败：%w", err)
		}
		for _, row := range rows {
			s.memSyncCache.DeleteByFileId(row.FileId)
		}
		clear(rows)
		rows = rows[:0]
		return nil
	}
	for _, file := range s.memSyncCache.GetAllFile() {
		if err := s.Context.Err(); err != nil {
			return err
		}
		next := file.GetSyncFile(s, s.Account.BaseUrl)
		if s.ledgerProtected(next) || (file.SourceType == models.SourceType115 && file.Path == "") {
			continue
		}
		rows = append(rows, *next)
		if len(rows) == syncLedgerPageSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := flush(); err != nil {
		return err
	}
	// 新事实全部落库后再移除精确旧行；中途写入失败时保留旧事实。
	for start := 0; start < len(supersededIDs); start += syncLedgerPageSize {
		ids := supersededIDs[start:min(start+syncLedgerPageSize, len(supersededIDs))]
		if err := syncPositionTransaction(ledgerDB, true, func(tx *gorm.DB) error {
			return tx.Where("sync_path_id = ? AND id IN ?", s.SyncPathId, ids).Delete(&models.SyncFile{}).Error
		}); err != nil {
			return fmt.Errorf("删除已替换的百度旧路径记录失败：%w", err)
		}
	}
	s.Sync.Logger.Infof("同步文件记录保存完成，缓存中剩余 %d 条受保护数据", s.memSyncCache.Count())
	return s.Context.Err()
}

func updateSyncLedgerRows(tx *gorm.DB, syncPathID uint, rows []syncLedgerUpdate, updatedAt int64) error {
	batchSize := syncLedgerPageSize
	if tx.Dialector.Name() == "sqlite" {
		batchSize = 32
	}
	for start := 0; start < len(rows); start += batchSize {
		batch := rows[start:min(start+batchSize, len(rows))]
		ids := make([]uint, len(batch))
		for i, row := range batch {
			ids[i] = row.id
		}
		updates := map[string]any{"updated_at": updatedAt}
		for i, column := range syncLedgerColumns {
			var sql strings.Builder
			sql.WriteString("CASE id")
			args := make([]any, 0, len(batch)*2)
			for _, row := range batch {
				sql.WriteString(" WHEN ? THEN ?")
				args = append(args, row.id, row.values[i])
			}
			// ELSE 同时保留列类型，PostgreSQL 可据此识别空字符串及数字参数。
			sql.WriteString(" ELSE " + column + " END")
			updates[column] = gorm.Expr(sql.String(), args...)
		}
		if err := tx.Model(&models.SyncFile{}).Where("sync_path_id = ? AND id IN ?", syncPathID, ids).Updates(updates).Error; err != nil {
			return err
		}
	}
	return nil
}

// syncPositionTransaction 将失效区间包住提交，包括失败和提交结果不确定的退出。
func syncPositionTransaction(handle *gorm.DB, changed bool, write func(*gorm.DB) error) error {
	if changed {
		finish := models.BeginSyncPositionMutation()
		defer finish()
	}
	return handle.Transaction(write)
}
