package models

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"gorm.io/gorm"
)

// CreateRestoreSchema 在恢复事务中建齐所选模型及关系约束，不修补或转换业务记录。
func CreateRestoreSchema(tx *gorm.DB, tables []any) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	if len(tables) == 0 {
		return nil
	}
	return tx.AutoMigrate(tables...)
}

// EnsureRestoreIndexes 仅创建恢复表所需的显式索引，重复活跃任务应导致恢复失败。
func EnsureRestoreIndexes(tx *gorm.DB, tables []any) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	for _, table := range tables {
		stmt := &gorm.Statement{DB: tx}
		if err := stmt.Parse(table); err != nil {
			return fmt.Errorf("读取恢复模型结构失败：%w", err)
		}
		var err error
		switch stmt.Schema.Table {
		case "db_download_tasks":
			err = createActiveDownloadTaskUniqueIndex(tx)
		case "db_upload_tasks":
			err = createActiveUploadTaskUniqueIndex(tx)
		case "sync_files":
			err = EnsureSyncFileLookupIndexes(tx)
		case "strm_generation_tasks":
			err = EnsureStrmGenerationQueueIndex(tx)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func createActiveDownloadTaskUniqueIndex(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_db_download_tasks_active_target
		ON db_download_tasks (source, source_type, account_id, dedup_scope_hash, dedup_locator_hash)
		WHERE dedup_scope_hash IS NOT NULL AND dedup_scope_hash <> ''
			AND dedup_locator_hash IS NOT NULL AND dedup_locator_hash <> ''
			AND status IN (0, 1)`).Error; err != nil {
		return fmt.Errorf("创建活跃下载任务唯一索引失败：%w", err)
	}
	return nil
}

func createActiveUploadTaskUniqueIndex(tx *gorm.DB) error {
	if err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_db_upload_tasks_active_target
		ON db_upload_tasks (source, source_type, account_id, remote_full_path)
		WHERE remote_full_path IS NOT NULL AND remote_full_path <> '' AND status IN (0, 1, 5, 6)`).Error; err != nil {
		return fmt.Errorf("创建活跃上传任务唯一索引失败：%w", err)
	}
	return nil
}

// RepairSequencesTx 在调用方事务中修复所选模型的自增主键，失败由调用方整体回滚。
func RepairSequencesTx(tx *gorm.DB, tables []any) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	if tx.Dialector.Name() != "postgres" {
		return nil
	}
	for _, table := range tables {
		stmt := &gorm.Statement{DB: tx}
		if err := stmt.Parse(table); err != nil {
			return fmt.Errorf("读取恢复模型结构失败：%w", err)
		}
		for _, field := range stmt.Schema.PrimaryFields {
			if !field.AutoIncrement {
				continue
			}
			if err := ResetSequenceTx(tx, stmt.Schema.Table, field.DBName); err != nil {
				return fmt.Errorf("修复表 %s 的主键序列失败：%w", stmt.Schema.Table, err)
			}
		}
	}
	return nil
}

// ResetSequenceTx 在事务内阻止表写入后读取最大主键，使用可回滚的 RESTART 更新序列。
// 已分配但尚未存为业务行的序列值仍保留，避免日常修复回退序列高水位。
func ResetSequenceTx(tx *gorm.DB, tableName, columnName string) error {
	if tx == nil {
		return errors.New("数据库连接为空")
	}
	stmt := &gorm.Statement{DB: tx}
	quotedTable, quotedColumn := stmt.Quote(tableName), stmt.Quote(columnName)
	if tx.Dialector.Name() == "postgres" {
		if err := tx.Exec("LOCK TABLE " + quotedTable + " IN SHARE ROW EXCLUSIVE MODE").Error; err != nil {
			return fmt.Errorf("锁定主键序列对应表失败：%w", err)
		}
	}
	var maxID sql.NullInt64
	if err := tx.Table(tableName).Select("MAX(" + quotedColumn + ")").Scan(&maxID).Error; err != nil {
		return err
	}
	if tx.Dialector.Name() != "postgres" {
		// SQLite 在导入显式主键时自动更新 sqlite_sequence。
		return nil
	}
	var sequence struct {
		SchemaName   string
		SequenceName string
		Increment    int64
		MaxValue     int64
	}
	if err := tx.Raw(`SELECT ns.nspname AS schema_name, cls.relname AS sequence_name,
		seq.seqincrement AS increment, seq.seqmax AS max_value
		FROM pg_sequence seq
		JOIN pg_class cls ON cls.oid = seq.seqrelid
		JOIN pg_namespace ns ON ns.oid = cls.relnamespace
		WHERE seq.seqrelid = pg_get_serial_sequence(?, ?)::regclass`, quotedTable, columnName).Scan(&sequence).Error; err != nil {
		return fmt.Errorf("读取主键序列定义失败：%w", err)
	}
	if sequence.SequenceName == "" {
		return nil
	}
	if sequence.Increment != 1 {
		return fmt.Errorf("主键序列步长 %d 不受支持", sequence.Increment)
	}
	quotedSequence := stmt.Quote(sequence.SchemaName) + "." + stmt.Quote(sequence.SequenceName)
	var state struct {
		LastValue int64
		IsCalled  bool
	}
	if err := tx.Raw("SELECT last_value, is_called FROM " + quotedSequence).Scan(&state).Error; err != nil {
		return fmt.Errorf("读取主键序列当前位置失败：%w", err)
	}
	if state.IsCalled && state.LastValue == sequence.MaxValue {
		// 已耗尽的序列不回退；历史记录仍能保留，后续分配继续报告序列耗尽。
		return nil
	}
	nextValue := state.LastValue
	if state.IsCalled {
		nextValue++
	}
	exhausted := maxID.Valid && maxID.Int64 >= sequence.MaxValue
	if exhausted {
		nextValue = sequence.MaxValue
	} else if maxID.Valid && maxID.Int64 >= nextValue {
		nextValue = maxID.Int64 + 1
	}
	// 标识符来自模型及 PostgreSQL 系统目录；数值来自 int64，不能通过占位符传给 RESTART。
	if err := tx.Exec("ALTER SEQUENCE " + quotedSequence + " RESTART WITH " + strconv.FormatInt(nextValue, 10)).Error; err != nil {
		return err
	}
	if exhausted {
		// 最大主键本身是有效备份数据。先 RESTART 再消耗上界，使后续分配报耗尽而非主键冲突；
		// 此处 nextval 属于本事务 RESTART 后的新序列状态，回滚会恢复 RESTART 之前的状态。
		var consumed int64
		return tx.Raw("SELECT nextval(?::regclass)", quotedSequence).Scan(&consumed).Error
	}
	return nil
}
