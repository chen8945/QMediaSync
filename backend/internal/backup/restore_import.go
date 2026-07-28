package backup

import (
	"bufio"
	"fmt"
	"io"
	"reflect"

	"qmediasync/internal/models"

	"gorm.io/gorm"
)

// importBatchSize 是导入时每批写入的记录数，避免整表载入内存或产生超大语句。
const importBatchSize = 200

// importCatalogJSONL 把一张表的 JSON Lines 流导入指定连接。
// 它是恢复提交和自动回滚共用的唯一导入实现，两条路径因此具有相同的失败语义。
func importCatalogJSONL(database *gorm.DB, entry models.TableCatalogEntry, source io.Reader) (int64, error) {
	modelType := reflect.TypeOf(entry.Model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	codec, err := newArtifactRecordCodec(entry.Model)
	if err != nil {
		return 0, fmt.Errorf("读取表 %s 的模型结构: %w", entry.ID, err)
	}
	sliceType := reflect.SliceOf(reflect.PointerTo(modelType))
	batch := reflect.MakeSlice(sliceType, 0, importBatchSize)

	scanner := bufio.NewScanner(source)
	scanner.Buffer(make([]byte, 64*1024), artifactMaxJSONLineSize+1)
	var imported int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 || len(line) > artifactMaxJSONLineSize {
			return 0, fmt.Errorf("%w：JSONL 内容无效", ErrInvalidArtifact)
		}
		record, err := codec.unmarshalRecord(line)
		if err != nil {
			// 单行解析失败必须终止整次导入：静默跳过正是旧恢复实现丢数据的原因。
			return 0, fmt.Errorf("%w：表 %s 的记录无法解析", ErrInvalidArtifact, entry.ID)
		}
		batch = reflect.Append(batch, record)
		if batch.Len() < importBatchSize {
			continue
		}
		if err := insertCatalogBatch(database, batch); err != nil {
			return 0, err
		}
		imported += int64(batch.Len())
		batch = reflect.MakeSlice(sliceType, 0, importBatchSize)
	}
	if err := scanner.Err(); err != nil {
		return 0, fmt.Errorf("%w：JSONL 行超出限制或损坏", ErrInvalidArtifact)
	}
	if batch.Len() > 0 {
		if err := insertCatalogBatch(database, batch); err != nil {
			return 0, err
		}
		imported += int64(batch.Len())
	}
	return imported, nil
}

func insertCatalogBatch(database *gorm.DB, batch reflect.Value) error {
	// 主键随记录一起导入，保持跨表引用的 ID 关系。
	// 目标表在导入前已经清空，因此这里不做任何冲突兜底：主键冲突只可能来自损坏的工件，必须让导入失败。
	if err := database.Session(&gorm.Session{SkipHooks: true}).
		CreateInBatches(batch.Interface(), importBatchSize).Error; err != nil {
		return fmt.Errorf("导入数据失败: %w", err)
	}
	return nil
}

// clearCatalogTables 清空目标库中的应用表。
// 顺序与导入顺序相反，使潜在的引用关系先被移除。
func clearCatalogTables(database *gorm.DB, catalog []models.TableCatalogEntry) error {
	for index := len(catalog) - 1; index >= 0; index-- {
		entry := catalog[index]
		if !database.Migrator().HasTable(entry.Model) {
			continue
		}
		if err := database.Session(&gorm.Session{SkipHooks: true, AllowGlobalUpdate: true}).
			Where("1 = 1").Delete(entry.Model).Error; err != nil {
			return fmt.Errorf("清空表 %s 失败: %w", entry.ID, err)
		}
	}
	return nil
}

// repairSequences 修复 PostgreSQL 自增序列，使导入既有主键后的新插入不会冲突。
func repairSequences(database *gorm.DB, catalog []models.TableCatalogEntry) error {
	for _, entry := range catalog {
		if !database.Migrator().HasTable(entry.Model) {
			continue
		}
		if err := models.ResetSequenceWithDB(database, entry.PhysicalName, "id"); err != nil {
			return fmt.Errorf("修复表 %s 的序列失败: %w", entry.ID, err)
		}
	}
	return nil
}

// invalidateUserSessions 在数据导入后清空浏览器会话，使恢复后的所有会话必须重新登录。
func invalidateUserSessions(database *gorm.DB) error {
	if !database.Migrator().HasTable(&models.UserSession{}) {
		return nil
	}
	if err := database.Session(&gorm.Session{SkipHooks: true, AllowGlobalUpdate: true}).
		Where("1 = 1").Delete(&models.UserSession{}).Error; err != nil {
		return fmt.Errorf("失效浏览器会话失败: %w", err)
	}
	return nil
}
