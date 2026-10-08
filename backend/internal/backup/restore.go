package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"
)

// Restore 从逻辑备份恢复数据库，保留本机备份历史，其余恢复表在同一事务内替换。
func Restore(filePath string) error {
	if err := beginTask("restore"); err != nil {
		return err
	}
	return runTask(func() error { return restore(filePath) })
}

type restoreTable struct {
	model    any
	schema   *schema.Schema
	readRows func(func(map[string]any) error) error
	rowCount int64
}

type restorePlan struct {
	tables []restoreTable
	legacy bool
}

// 数据库错误可能含约束冲突的完整字段值；公开及服务日志只保留恢复位置。
type restoreDatabaseError struct {
	operation string
	cause     error
}

func (err *restoreDatabaseError) Error() string { return err.operation }
func (err *restoreDatabaseError) Unwrap() error { return err.cause }

func restoreDBError(operation string, cause error) error {
	return &restoreDatabaseError{operation: operation, cause: cause}
}

func restore(filePath string) (err error) {
	return restoreArchive(filePath, false)
}

func restoreArchive(filePath string, publishUpload bool) (err error) {
	backupDir := filepath.Join(helpers.ConfigDir, "backups")
	if err := helpers.EnsurePrivateDir(helpers.ConfigDir, "backups"); err != nil {
		return fmt.Errorf("创建恢复目录失败：%w", err)
	}
	tempDir, err := os.MkdirTemp(backupDir, "backup-restore-*")
	if err != nil {
		return fmt.Errorf("创建临时目录失败：%w", err)
	}
	defer os.RemoveAll(tempDir)
	if err := extractBackupArchive(filePath, tempDir); err != nil {
		return fmt.Errorf("解压文件失败：%w", err)
	}
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("读取临时目录失败：%w", err)
	}
	inputDir := tempDir
	if len(entries) == 1 && entries[0].IsDir() {
		inputDir = filepath.Join(tempDir, entries[0].Name())
	}
	SetRunningResult("restore", "校验备份文件和数据库版本", len(models.AllTables), 0, "")
	plan, err := prepareRestorePlan(inputDir, db.Db)
	if err != nil {
		return err
	}
	if publishUpload {
		// 解压结果已经通过完整预检；发布后的归档即使数据库恢复失败也保留。
		if _, err := publishUploadedBackup(filePath); err != nil {
			return err
		}
	}
	maintenance, err := beginRestoreMaintenance(context.Background())
	if err != nil {
		return err
	}
	outcome := "not_started"
	defer func() { err = errors.Join(err, maintenance.Finish(outcome == "committed", outcome == "uncertain")) }()
	finishPosition := models.BeginSyncPositionMutation()
	defer finishPosition()
	// 即使驱动在提交或回滚本身 panic，也不能把未知结果误报成未修改数据库。
	outcome = "uncertain"
	progressMu.Lock()
	restoreOutcome = outcome
	progressMu.Unlock()
	outcome, err = executeRestorePlan(maintenance.Database(), plan)
	progressMu.Lock()
	restoreOutcome = outcome
	progressMu.Unlock()
	if err != nil {
		return err
	}
	SetRunningResult("restore", "数据库恢复事务已提交", len(plan.tables), len(plan.tables), "")
	helpers.AppLogger.Infof("完成恢复任务")
	return nil
}

func prepareRestorePlan(dir string, database *gorm.DB) (*restorePlan, error) {
	var manifest *logicalManifest
	_, err := os.Stat(filepath.Join(dir, logicalManifestFile))
	if err == nil {
		manifest, err = prepareLogicalBackup(dir, database)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	plan := &restorePlan{legacy: manifest == nil}
	if manifest != nil {
		for _, table := range manifest.Tables {
			entry, err := newRestoreTable(database, table.Model)
			if err != nil {
				return nil, err
			}
			// prepareLogicalBackup 已校验全部归档内容；本机历史不参与后续 DDL、导入或序列重置。
			if entry.schema.Table == "backup_record" {
				continue
			}
			entry.rowCount = table.RowCount
			entry.readRows = func(consume func(map[string]any) error) error {
				return readLogicalRows(dir, table, consume)
			}
			plan.tables = append(plan.tables, entry)
		}
	} else {
		helpers.AppLogger.Warnf("正在恢复旧格式备份：旧包未保存的字段无法补回，缺少的业务表保持原状")
		for _, model := range models.AllTables {
			if restoreRebuildsModel(model) {
				continue
			}
			entry, exists, err := prepareLegacyRestoreTable(dir, helpers.GetStructName(model), model, database)
			if err != nil {
				return nil, err
			}
			if exists && entry.schema.Table != "backup_record" {
				plan.tables = append(plan.tables, entry)
			} else if entry.schema.Table == "migrator" {
				return nil, fmt.Errorf("%w：旧备份缺少 Migrator.json，无法确认数据库版本", ErrArchiveInvalid)
			}
		}
	}
	if len(plan.tables) == 0 {
		return nil, fmt.Errorf("%w：备份中没有可恢复的模型文件", ErrArchiveInvalid)
	}
	// 浏览器会话一律清空；派生索引只从恢复后的原始记录重建。
	for _, model := range models.AllTables {
		if restoreRebuildsModel(model) {
			entry, err := newRestoreTable(database, model)
			if err != nil {
				return nil, err
			}
			plan.tables = append(plan.tables, entry)
		}
	}
	if err := orderRestoreTables(plan); err != nil {
		return nil, err
	}
	if manifest == nil {
		if err := checkLegacyRestoreDependencies(database, plan); err != nil {
			return nil, err
		}
	}
	if err := checkRestoreLocalSecrets(plan); err != nil {
		return nil, err
	}
	return plan, nil
}

func checkRestoreLocalSecrets(plan *restorePlan) error {
	for _, table := range plan.tables {
		if table.schema.Table != "users" || table.readRows == nil {
			continue
		}
		if err := table.readRows(func(row map[string]any) error {
			for _, name := range []string{"two_factor_secret", "two_factor_pending_secret"} {
				secret, ok := row[name].(string)
				if !ok || secret == "" {
					continue
				}
				if _, err := helpers.DecryptLocalSecretWithCurrentKey(secret); err != nil {
					// 不保留底层解密错误、密文或明文，避免日志泄露认证数据。
					return ErrRestoreKeyMismatch
				}
			}
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// executeRestorePlan 保留回滚结果，提交/回滚结果不确定时不能声称旧库完整。
func executeRestorePlan(database *gorm.DB, plan *restorePlan) (outcome string, err error) {
	outcome = "not_started"
	tx := database.Session(&gorm.Session{Logger: logger.Default.LogMode(logger.Silent)}).Begin()
	if tx.Error != nil {
		return outcome, restoreDBError("开始恢复事务失败", tx.Error)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			outcome = "uncertain"
			rollbackErr := tx.Rollback().Error
			if rollbackErr == nil {
				outcome = "rolled_back"
			}
			err = errors.New("恢复事务异常")
			if rollbackErr != nil {
				err = errors.Join(err, restoreDBError("回滚恢复事务失败", rollbackErr))
			}
		}
	}()
	if err := applyRestorePlan(tx, plan); err != nil {
		if rollbackErr := tx.Rollback().Error; rollbackErr != nil {
			return "uncertain", errors.Join(err, restoreDBError("回滚恢复事务失败", rollbackErr))
		}
		return "rolled_back", err
	}
	if err := tx.Commit().Error; err != nil {
		return "uncertain", restoreDBError("提交恢复事务失败", err)
	}
	return "committed", nil
}

func restoreRebuildsModel(model any) bool {
	switch model.(type) {
	case models.UserSession, *models.UserSession,
		models.EmbyObservedEvidenceIndex, *models.EmbyObservedEvidenceIndex,
		models.EmbyItemMembership, *models.EmbyItemMembership:
		return true
	default:
		return false
	}
}

func newRestoreTable(database *gorm.DB, model any) (restoreTable, error) {
	statement := &gorm.Statement{DB: database}
	if err := statement.Parse(model); err != nil {
		return restoreTable{}, fmt.Errorf("解析恢复模型失败：%w", err)
	}
	return restoreTable{model: model, schema: statement.Schema}, nil
}

// orderRestoreTables 按模型声明的外键依赖排序，不另维护一份表名清单。
func orderRestoreTables(plan *restorePlan) error {
	byName := make(map[string]restoreTable, len(plan.tables))
	for _, table := range plan.tables {
		if _, exists := byName[table.schema.Table]; exists {
			return fmt.Errorf("恢复表重复：%s", table.schema.Table)
		}
		byName[table.schema.Table] = table
	}
	visited := make(map[string]bool)
	visiting := make(map[string]bool)
	ordered := make([]restoreTable, 0, len(plan.tables))
	var visit func(restoreTable) error
	visit = func(table restoreTable) error {
		name := table.schema.Table
		if visited[name] {
			return nil
		}
		if visiting[name] {
			return fmt.Errorf("恢复模型存在循环外键依赖：%s", name)
		}
		visiting[name] = true
		for _, relation := range table.schema.Relationships.Relations {
			constraint := relation.ParseConstraint()
			if constraint == nil || constraint.Schema.Table != name || constraint.ReferenceSchema.Table == name {
				continue
			}
			if dependency, exists := byName[constraint.ReferenceSchema.Table]; exists {
				if err := visit(dependency); err != nil {
					return err
				}
			}
		}
		visiting[name], visited[name] = false, true
		ordered = append(ordered, table)
		return nil
	}
	for _, table := range plan.tables {
		if err := visit(table); err != nil {
			return err
		}
	}
	plan.tables = ordered
	return nil
}

// 旧包缺少无关表时保留目标数据；不允许删除父表时悄悄移除保留表的外键。
func checkLegacyRestoreDependencies(database *gorm.DB, plan *restorePlan) error {
	replaced := make(map[string]bool, len(plan.tables))
	for _, table := range plan.tables {
		replaced[table.schema.Table] = true
	}
	for _, model := range models.AllTables {
		table, err := newRestoreTable(database, model)
		if err != nil {
			return err
		}
		if replaced[table.schema.Table] || !database.Migrator().HasTable(model) {
			continue
		}
		for _, relation := range table.schema.Relationships.Relations {
			constraint := relation.ParseConstraint()
			if constraint != nil && constraint.Schema.Table == table.schema.Table && replaced[constraint.ReferenceSchema.Table] {
				return fmt.Errorf("%w：旧备份缺少关联表 %s，不能单独替换其引用的 %s", ErrArchiveUnsupported, table.schema.Table, constraint.ReferenceSchema.Table)
			}
		}
	}
	return nil
}

func applyRestorePlan(tx *gorm.DB, plan *restorePlan) error {
	if plan.legacy {
		if err := checkLegacyRestoreDependencies(tx, plan); err != nil {
			return err
		}
	}
	allModels := make([]any, 0, len(plan.tables))
	for _, table := range plan.tables {
		allModels = append(allModels, table.model)
	}
	// 先删完旧表，再一次建齐结构，避免随后删除父表时级联移除新外键。
	for i := len(plan.tables) - 1; i >= 0; i-- {
		if err := tx.Migrator().DropTable(plan.tables[i].model); err != nil {
			return restoreDBError(fmt.Sprintf("删除表 %s 失败", plan.tables[i].schema.Table), err)
		}
	}
	if err := models.CreateRestoreSchema(tx, allModels); err != nil {
		return restoreDBError("创建恢复表结构失败", err)
	}
	for i, table := range plan.tables {
		if table.readRows == nil {
			continue
		}
		var imported int64
		if err := table.readRows(func(row map[string]any) error {
			// map 写入只使用显式数据库列，不执行模型 hooks、默认值或自动时间赋值。
			if err := tx.Session(&gorm.Session{SkipHooks: true, SkipDefaultTransaction: true}).
				Table(table.schema.Table).Create(&row).Error; err != nil {
				return restoreDBError(fmt.Sprintf("%s 第 %d 行写入失败", table.schema.Table, imported+1), err)
			}
			imported++
			return nil
		}); err != nil {
			return err
		}
		if imported != table.rowCount {
			return fmt.Errorf("表 %s 导入行数不符", table.schema.Table)
		}
		SetRunningResult("restore", fmt.Sprintf("已校验导入 %s：%d 条，等待事务提交", table.schema.Table, imported), len(plan.tables), i+1, "")
	}
	// 即使旧格式未包含会话文件、或测试/扩展表清单未列出会话，也撤销目标浏览器会话。
	if tx.Migrator().HasTable(&models.UserSession{}) {
		if err := tx.Where("1 = 1").Delete(&models.UserSession{}).Error; err != nil {
			return restoreDBError("清空浏览器会话失败", err)
		}
	}
	if err := models.EnsureRestoreIndexes(tx, allModels); err != nil {
		return restoreDBError("重建恢复索引失败", err)
	}
	for _, table := range plan.tables {
		switch table.model.(type) {
		case models.EmbyObservedEvidenceIndex, *models.EmbyObservedEvidenceIndex:
			if err := models.RebuildEmbyObservedEvidenceIndex(tx); err != nil {
				return restoreDBError("重建 Emby 观察索引失败", err)
			}
		case models.EmbyItemMembership, *models.EmbyItemMembership:
			if err := models.RebuildEmbyItemMembership(tx); err != nil {
				return restoreDBError("重建 Emby 成员索引失败", err)
			}
		}
	}
	if err := models.RepairSequencesTx(tx, allModels); err != nil {
		return restoreDBError("重置恢复主键序列失败", err)
	}
	if tx.Dialector.Name() == "sqlite" {
		// SQLite 连接可能未开启即时外键检查，提交前仍必须验证恢复的引用完整性。
		statement := &gorm.Statement{DB: tx}
		for _, table := range plan.tables {
			var violations []struct{ Table string }
			if err := tx.Raw("PRAGMA foreign_key_check(" + statement.Quote(table.schema.Table) + ")").Scan(&violations).Error; err != nil {
				return restoreDBError(fmt.Sprintf("检查表 %s 的外键失败", table.schema.Table), err)
			}
			if len(violations) != 0 {
				return fmt.Errorf("恢复表 %s 存在缺失的外键引用", table.schema.Table)
			}
		}
	}
	return nil
}

func prepareLegacyRestoreTable(dir, modelName string, model any, database *gorm.DB) (restoreTable, bool, error) {
	table, err := newRestoreTable(database, model)
	if err != nil {
		return table, false, err
	}
	logical, err := describeLogicalTable(database, model)
	if err != nil {
		return table, false, err
	}
	path := filepath.Join(dir, modelName+".json")
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return table, false, nil
		}
		return table, false, fmt.Errorf("读取 %s 备份失败：%w", modelName, err)
	}
	table.readRows = func(consume func(map[string]any) error) error {
		return readLegacyRows(path, table, consume)
	}
	if err := table.readRows(func(row map[string]any) error {
		if err := validateLogicalTargetRow(database, logical, row); err != nil {
			return fmt.Errorf("%w：旧备份表 %s 第 %d 行：%w", ErrArchiveInvalid, table.schema.Table, table.rowCount+1, err)
		}
		table.rowCount++
		return nil
	}); err != nil {
		return table, false, err
	}
	if table.schema.Table == "migrator" && table.rowCount != 1 {
		return table, false, fmt.Errorf("%w：旧备份版本记录必须有且仅有一条", ErrArchiveInvalid)
	}
	return table, true, nil
}

func adaptLegacyVersion(row map[string]any) error {
	version, ok := row["version_code"].(int64)
	if ok && version == int64(models.MaxVersionCode) {
		return nil
	}
	// 66 → 67 只放宽文本列，旧逻辑值可直接导入；与其他历史迁移保持明确边界。
	if ok && version == 66 && models.MaxVersionCode == 67 {
		row["version_code"] = int64(67)
		return nil
	}
	return fmt.Errorf("%w：旧备份数据库版本不兼容，需先使用对应版本程序升级后重新备份", ErrArchiveUnsupported)
}

func readLegacyRows(path string, table restoreTable, consume func(map[string]any) error) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("打开 %s 备份失败：%w", table.schema.Table, err)
	}
	defer file.Close()
	lineNumber := 0
	return readBackupRows(file, maxBackupRowSize, func(raw json.RawMessage) error {
		lineNumber++
		row, err := decodeLegacyRow(raw, table)
		if err != nil {
			return fmt.Errorf("%w：%s 第 %d 行：%w", ErrArchiveInvalid, table.schema.Table, lineNumber, err)
		}
		if table.schema.Table == "migrator" {
			if err := adaptLegacyVersion(row); err != nil {
				return err
			}
		}
		return consume(row)
	})
}

func decodeLegacyRow(line []byte, table restoreTable) (map[string]any, error) {
	if err := validateJSONUnicode(line); err != nil {
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return nil, fmt.Errorf("解析 JSON 失败：%w", err)
	}
	if raw == nil {
		return nil, fmt.Errorf("记录必须是 JSON 对象")
	}
	item := reflect.New(table.schema.ModelType)
	if err := json.Unmarshal(line, item.Interface()); err != nil {
		return nil, fmt.Errorf("解析旧格式记录失败：%w", err)
	}
	row := make(map[string]any, len(table.schema.DBNames))
	for _, field := range table.schema.Fields {
		if field.DBName == "" {
			continue
		}
		value, _ := field.ValueOf(context.Background(), item)
		if field.Serializer != nil {
			// ValueOf 对 serializer 字段返回 driver.Valuer 包装器，旧 JSON 需要实际模型值。
			value = field.ReflectValueOf(context.Background(), item).Interface()
		}
		encoded, exists := raw[field.DBName]
		if !exists {
			jsonName := strings.Split(field.StructField.Tag.Get("json"), ",")[0]
			if jsonName == "" {
				jsonName = field.Name
			}
			for name, candidate := range raw {
				if jsonName != "-" && strings.EqualFold(name, jsonName) {
					encoded, exists = candidate, true
					break
				}
			}
		}
		if exists {
			if bytes.Equal(bytes.TrimSpace(encoded), []byte("null")) {
				value = nil
			} else {
				typed := reflect.New(field.FieldType)
				if err := json.Unmarshal(encoded, typed.Interface()); err != nil {
					return nil, fmt.Errorf("列 %s 类型不兼容：%w", field.DBName, err)
				}
				value = typed.Elem().Interface()
			}
		} else if table.schema.Table == "users" && field.DBName == "singleton_key" {
			// 旧包未导出单例键，原 BeforeCreate 的固定值是旧格式唯一的兼容补值。
			value = uint8(1)
		}
		column, err := describeLogicalColumn(field)
		if err != nil {
			return nil, fmt.Errorf("列 %s 类型不兼容：%w", field.DBName, err)
		}
		if field.Serializer != nil {
			if _, ok := field.Serializer.(schema.JSONSerializer); !ok {
				return nil, fmt.Errorf("列 %s 的旧格式序列化方式尚未定义适配", field.DBName)
			}
			if value != nil {
				// 旧备份是模型 JSON 对象；先转回 JSON serializer 的数据库文本，NULL 仍是 NULL。
				value, err = field.Serializer.Value(context.Background(), field, item, value)
				if err != nil {
					return nil, fmt.Errorf("列 %s 的旧格式 JSON 序列化失败：%w", field.DBName, err)
				}
			}
		}
		if column.Type == "timestamp" && value != nil {
			indirect := reflect.ValueOf(value)
			for indirect.Kind() == reflect.Pointer && !indirect.IsNil() {
				indirect = indirect.Elem()
			}
			if timestamp, ok := indirect.Interface().(time.Time); ok {
				value = timestamp.UTC().Round(time.Microsecond)
			}
		}
		encodedValue, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("列 %s 无法转换：%w", field.DBName, err)
		}
		value, err = decodeLogicalValue(column, encodedValue)
		if err != nil {
			return nil, fmt.Errorf("列 %s 无法转换：%w", field.DBName, err)
		}
		row[field.DBName] = value
	}
	if table.schema.Table == "users" && row["two_factor_enabled"] == true {
		if secret, ok := row["two_factor_secret"].(string); !ok || secret == "" {
			return nil, fmt.Errorf("旧备份缺少已启用两步验证的密钥，请在源实例升级后重新备份")
		}
	}
	if table.schema.Table == "api_keys" && row["key_hash"] == "" {
		return nil, fmt.Errorf("旧备份缺少 API Key 认证字段，请在源实例升级后重新备份")
	}
	return row, nil
}
