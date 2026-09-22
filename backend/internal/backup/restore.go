package backup

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// 从文件还原到数据库
func Restore(filePath string) error {
	if err := beginTask("restore"); err != nil {
		return err
	}
	return runTask(func() error { return restore(filePath) })
}

func restore(filePath string) (err error) {
	totalTable := len(models.AllTables)
	count := 0
	// 检查文件是否存在
	if _, err := os.Stat(filePath); err != nil {
		return fmt.Errorf("读取备份文件失败：%w", err)
	}
	// 停止所有任务
	if err := pauseTasks(); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, resumeTasks()) }()
	// 解压到临时目录
	tempDir, err := os.MkdirTemp(filepath.Join(helpers.ConfigDir, "backups"), "backup-restore-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(tempDir)
	// 解压文件
	if zerr := helpers.ExtractZip(filePath, tempDir); zerr != nil {
		return fmt.Errorf("解压文件失败：%w", zerr)
	}
	// 检查 tempDir 下是否只有一个文件夹
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败：%w", err)
	}
	if len(entries) == 1 && entries[0].IsDir() {
		helpers.AppLogger.Infof("备份文件解压后只有一个文件夹：%s，使用该文件夹作为入口目录", entries[0].Name())
		tempDir = filepath.Join(tempDir, entries[0].Name())
	}
	// 开始还原
	SetRunningResult("restore", "开始还原数据库", totalTable, count, "")
	var restoreErr error
	for _, table := range models.AllTables {
		if err := restoreFromJsonFile(tempDir, helpers.GetStructName(table), totalTable, &count, table); err != nil {
			restoreErr = errors.Join(restoreErr, err)
			continue
		}
	}
	if restoreErr != nil {
		return restoreErr
	}
	if count == 0 {
		return fmt.Errorf("备份中没有可恢复的模型文件")
	}
	helpers.AppLogger.Infof("完成恢复任务")
	return nil
}

// 从 JSON 文件还原到数据库
func restoreFromJsonFile(backupDir string, modelName string, totalTable int, count *int, model any) error {
	backupFilePath := filepath.Join(backupDir, modelName+".json")
	// 检查文件是否存在
	if _, err := os.Stat(backupFilePath); os.IsNotExist(err) {
		helpers.AppLogger.Warnf("备份文件不存在：%s", backupFilePath)
		return nil
	}
	// 读取文件，一行是一条 JSON
	file, err := os.Open(backupFilePath)
	if err != nil {
		helpers.AppLogger.Warnf("打开备份文件 %s 失败：%v", backupFilePath, err)
		return fmt.Errorf("打开备份文件 %s 失败：%v", backupFilePath, err)
	}
	defer file.Close()
	// 1. 删除表（如果存在）
	err = db.Db.Migrator().DropTable(model)
	if err != nil {
		// 处理错误
		helpers.AppLogger.Warnf("删除表 %s 失败：%v", modelName, err)
		return fmt.Errorf("删除表 %s 失败：%v", modelName, err)
	} else {
		helpers.AppLogger.Infof("表 %s 已删除", modelName)
	}

	var rowErrors error
	// 仅保留首个具体错误，避免损坏的大文件积累无界错误对象；仍继续恢复可用记录。
	rememberError := func(err error) {
		if rowErrors == nil {
			rowErrors = err
		}
	}
	// 2. 重新创建表
	err = db.Db.AutoMigrate(model)
	if err != nil {
		// 处理错误
		helpers.AppLogger.Warnf("创建表 %s 失败：%v", modelName, err)
		if strings.Contains(err.Error(), "index") {
			rememberError(fmt.Errorf("创建 %s 索引失败：%w", modelName, err))
			helpers.AppLogger.Infof("表 %s 索引创建失败，跳过错误，继续导入数据", modelName)
		} else {
			return fmt.Errorf("创建表 %s 失败：%v", modelName, err)
		}
	} else {
		helpers.AppLogger.Infof("表 %s 已创建", modelName)
	}
	// 读取文件内容
	scanner := bufio.NewScanner(file)
	// 单条记录可能含长文本；超过上限或读取故障必须作为恢复失败返回。
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	// 统计还原数量
	var restoredCount int
	typ := reflect.TypeOf(model)
	if typ.Kind() == reflect.Ptr {
		typ = typ.Elem()
	}
	setCount := 0
	for scanner.Scan() {
		line := scanner.Text()
		// 使用反射创建新实例
		item := reflect.New(typ).Interface()
		if err := json.Unmarshal([]byte(line), item); err != nil {
			rememberError(fmt.Errorf("%s 解析 JSON 失败：%w", modelName, err))
			continue
		} else {
			// 插入数据库
			if err := db.Db.Create(item).Error; err != nil {
				rememberError(fmt.Errorf("%s 插入数据库失败：%w", modelName, err))
				continue
			}
		}
		restoredCount++
		setCount++
		if setCount >= 10 {
			setCount = 0
			SetRunningResult("restore", fmt.Sprintf("已还原 %d 条 %s 记录", restoredCount, modelName), totalTable, *count, "")
		}
	}
	if err := scanner.Err(); err != nil {
		rememberError(fmt.Errorf("读取 %s 备份数据失败：%w", modelName, err))
	}
	tableName := models.GetTableName(model)
	// 重置表的主键序列
	if db.Db.Dialector.Name() == "postgres" {
		if err := models.ResetSequence(tableName, "id"); err != nil {
			rememberError(fmt.Errorf("修复 %s 主键序列失败：%w", modelName, err))
		}
	}
	if rowErrors != nil {
		return rowErrors
	}
	*count++
	SetRunningResult("restore", fmt.Sprintf("已还原 %d 条 %s 记录", restoredCount, modelName), totalTable, *count, "")
	return nil
}
