package backup

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"gorm.io/gorm"
)

// ExportedArtifact 描述一次成功发布的 v1 工件。
type ExportedArtifact struct {
	Path        string
	Size        int64
	TableCount  int
	RecordCount int64
}

// exportArtifact 在维护屏障和任务静止之后，从单一一致读视图导出 v1 工件。
// 它先把全部表和白名单文件写入受限暂存目录，校验通过后才原子发布到工件目录，
// 因此中途失败不会在备份目录留下可被恢复选中的半成品。
func exportArtifact(backupType string, password []byte, progress func(completed int, total int, message string)) (ExportedArtifact, error) {
	keyText, err := helpers.LocalEncryptionKeyText()
	if err != nil {
		return ExportedArtifact{}, fmt.Errorf("读取实例密钥: %w", err)
	}
	artifactID, err := newArtifactID()
	if err != nil {
		return ExportedArtifact{}, err
	}
	destinationPath, err := artifactDestinationPath(backupType, artifactID)
	if err != nil {
		return ExportedArtifact{}, err
	}

	staging, err := os.MkdirTemp(exportStagingRoot(), "export-*")
	if err != nil {
		return ExportedArtifact{}, fmt.Errorf("创建导出暂存目录: %w", err)
	}
	defer os.RemoveAll(staging)
	if err := os.Chmod(staging, 0o700); err != nil {
		return ExportedArtifact{}, fmt.Errorf("设置导出暂存目录权限: %w", err)
	}

	dataSources, recordCount, err := exportTableData(staging, progress)
	if err != nil {
		return ExportedArtifact{}, err
	}
	configSources, err := CollectArtifactConfigSources(helpers.ConfigDir)
	if err != nil {
		return ExportedArtifact{}, err
	}

	manifest := ArtifactManifest{
		FormatVersion:       ArtifactFormatVersion,
		ArtifactID:          artifactID,
		ApplicationVersion:  helpers.Version,
		SchemaVersion:       models.SchemaVersion,
		SourceEngine:        currentArtifactEngine(),
		TableCatalogVersion: ArtifactTableCatalogVersion,
	}
	innerArchivePath := filepath.Join(staging, "inner.zip")
	if _, err := CreateInnerArchive(innerArchivePath, manifest, append(dataSources, configSources...)); err != nil {
		return ExportedArtifact{}, err
	}

	_, err = BuildArtifact(ArtifactBuildOptions{
		Destination:        destinationPath,
		InnerArchivePath:   innerArchivePath,
		ArtifactID:         artifactID,
		CreatedAt:          time.Now().Unix(),
		ApplicationVersion: manifest.ApplicationVersion,
		SchemaVersion:      manifest.SchemaVersion,
		SourceEngine:       manifest.SourceEngine,
		EncryptionKey:      []byte(keyText),
		Password:           password,
	})
	if err != nil {
		return ExportedArtifact{}, err
	}
	info, err := os.Stat(destinationPath)
	if err != nil {
		return ExportedArtifact{}, fmt.Errorf("读取工件状态: %w", err)
	}
	return ExportedArtifact{
		Path:        destinationPath,
		Size:        info.Size(),
		TableCount:  len(dataSources),
		RecordCount: recordCount,
	}, nil
}

// exportTableData 在单一读事务中导出主表目录选定的全部表。
// 逐表独立读取不构成一致快照，因此这里必须共用同一个事务。
func exportTableData(staging string, progress func(completed int, total int, message string)) ([]ArtifactFileSource, int64, error) {
	catalog := models.RegularBackupRestoreTableCatalog()
	dataDir := filepath.Join(staging, "data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, 0, fmt.Errorf("创建导出数据目录: %w", err)
	}

	transaction, err := beginConsistentReadTransaction()
	if err != nil {
		return nil, 0, err
	}
	defer transaction.Rollback()

	sources := make([]ArtifactFileSource, 0, len(catalog))
	var totalRecords int64
	for index, entry := range catalog {
		archivePath := "data/" + entry.ID + ".jsonl"
		sourcePath := filepath.Join(dataDir, entry.ID+".jsonl")
		recordCount, err := WriteArtifactJSONL(sourcePath, func(writer *JSONLWriter) error {
			return streamTableRecords(transaction, entry.Model, writer)
		})
		if err != nil {
			return nil, 0, fmt.Errorf("导出表 %s: %w", entry.ID, err)
		}
		sources = append(sources, ArtifactFileSource{
			ArchivePath: archivePath,
			SourcePath:  sourcePath,
			RecordCount: recordCount,
		})
		totalRecords += recordCount
		if progress != nil {
			progress(index+1, len(catalog), fmt.Sprintf("已导出 %s，共 %d 条", entry.PhysicalName, recordCount))
		}
	}
	if err := transaction.Commit().Error; err != nil {
		return nil, 0, fmt.Errorf("结束一致读事务: %w", err)
	}
	return sources, totalRecords, nil
}

// beginConsistentReadTransaction 打开导出使用的一致读视图。
// PostgreSQL 使用 REPEATABLE READ READ ONLY；SQLite 在 WAL 下由读事务本身提供快照，
// 其驱动不接受显式隔离级别，因此使用默认读事务。
func beginConsistentReadTransaction() (*gorm.DB, error) {
	var transaction *gorm.DB
	if db.IsPostgres() {
		transaction = db.Db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else {
		transaction = db.Db.Begin()
	}
	if transaction.Error != nil {
		return nil, fmt.Errorf("开启一致读事务: %w", transaction.Error)
	}
	return transaction, nil
}

func streamTableRecords(transaction *gorm.DB, model any, writer *JSONLWriter) error {
	modelType := reflect.TypeOf(model)
	if modelType.Kind() == reflect.Ptr {
		modelType = modelType.Elem()
	}
	codec, err := newArtifactRecordCodec(model)
	if err != nil {
		return fmt.Errorf("读取模型结构: %w", err)
	}
	rows, err := transaction.Model(model).Order("id").Rows()
	if err != nil {
		return fmt.Errorf("查询数据: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		record := reflect.New(modelType).Interface()
		if err := transaction.ScanRows(rows, record); err != nil {
			return fmt.Errorf("读取数据行: %w", err)
		}
		if err := writer.Write(codec.recordMap(reflect.ValueOf(record))); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("遍历数据行: %w", err)
	}
	return nil
}

// currentArtifactEngine 返回当前实例的引擎标识；内嵌与外部 PostgreSQL 共享同一标识。
func currentArtifactEngine() string {
	if db.IsPostgres() {
		return string(helpers.DbEnginePostgres)
	}
	return string(helpers.DbEngineSqlite)
}

// exportStagingRoot 返回导出专用暂存根目录。
// 它与上传暂存目录分开，避免导出中间文件被恢复选择或定时清理误当作候选工件。
func exportStagingRoot() string {
	root := filepath.Join(helpers.ConfigDir, "tmp", "backup-export")
	if err := os.MkdirAll(root, 0o700); err != nil {
		helpers.AppLogger.Warnf("创建导出暂存根目录失败：%v", err)
	}
	return root
}

// artifactDestinationPath 生成工件发布路径。
// 文件名带工件标识后缀，保证同一秒内的多次备份不会互相覆盖。
func artifactDestinationPath(backupType string, artifactID string) (string, error) {
	directory := ArtifactDir()
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", fmt.Errorf("创建备份目录: %w", err)
	}
	name := fmt.Sprintf("backup_%s_%s_%s.zip", backupType, time.Now().Format("20060102_150405"), artifactID[:8])
	return filepath.Join(directory, name), nil
}
