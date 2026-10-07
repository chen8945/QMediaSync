package backup

import (
	"fmt"

	"qmediasync/internal/db"
	"qmediasync/internal/models"
)

// backupToJsonFile 为单表文件错误测试提供入口。
func backupToJsonFile(backupDir string, modelName string, totalTable int, count *int, model any) error {
	table, err := describeLogicalTable(db.Db, model)
	if err != nil {
		return err
	}
	table.File = modelName + ".json"
	remaining := maxBackupExpandedSize
	if err := writeLogicalTable(db.Db, backupDir, &table, &remaining); err != nil {
		return err
	}
	*count++
	SetRunningResult("backup", fmt.Sprintf("已备份 %s %d 条", modelName, table.RowCount), totalTable, *count, "")
	return nil
}

// restoreFromJsonFile 为单表恢复回归提供入口。
func restoreFromJsonFile(backupDir, modelName string, totalTable int, count *int, model any) error {
	if restoreRebuildsModel(model) {
		return nil
	}
	table, exists, err := prepareLegacyRestoreTable(backupDir, modelName, model, db.Db)
	if err != nil || !exists {
		return err
	}
	finishPosition := models.BeginSyncPositionMutation()
	defer finishPosition()
	if _, err := executeRestorePlan(db.Db, &restorePlan{tables: []restoreTable{table}}); err != nil {
		return err
	}
	*count++
	SetRunningResult("restore", fmt.Sprintf("已还原 %d 条 %s 记录", table.rowCount, modelName), totalTable, *count, "")
	return nil
}
