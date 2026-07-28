package backup

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"gorm.io/gorm"
)

// restoreProgress 报告恢复提交的非敏感进度，不含路径或数据库标识。
type restoreProgress func(completed int, total int, message string)

// applyRestoreArtifact 在预恢复快照就绪后，把工件写入备份配置指定的目标。
// 顺序固定为：目标数据库 → 白名单配置与证书 → 日志；每一步之间记录不可逆阶段。
func applyRestoreArtifact(
	coordinator *OperationCoordinator,
	operationID string,
	target RestoreTarget,
	verified VerifiedArtifact,
	progress restoreProgress,
) error {
	reader, err := openInnerArchive(verified.InnerArchivePath, verified.Manifest)
	if err != nil {
		return err
	}
	defer reader.Close()

	if err := applyRestoreDatabase(target, reader, progress); err != nil {
		return err
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseDatabaseSwitched); err != nil {
		return fmt.Errorf("记录数据库切换阶段: %w", err)
	}

	if err := mirrorWhitelistFiles(reader, false); err != nil {
		return err
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseConfigSwitched); err != nil {
		return fmt.Errorf("记录配置切换阶段: %w", err)
	}

	if err := mirrorWhitelistFiles(reader, true); err != nil {
		return err
	}
	if err := coordinator.RecordPhase(operationID, OperationPhaseLogsSwitched); err != nil {
		return fmt.Errorf("记录日志切换阶段: %w", err)
	}
	return nil
}

func applyRestoreDatabase(target RestoreTarget, reader *innerArchiveReader, progress restoreProgress) error {
	switch target.Engine {
	case string(helpers.DbEngineSqlite):
		return applyRestoreSqlite(target, reader, progress)
	case string(helpers.DbEnginePostgres):
		return applyRestorePostgres(target, reader, progress)
	default:
		return fmt.Errorf("%w：目标数据库引擎无效", ErrRestoreTargetIncompatible)
	}
}

// applyRestoreSqlite 在目标文件同一目录的临时数据库完成导入与校验后原子切换。
// 临时数据库与目标处于同一文件系统，因此 rename 是原子的：中断只会留下切换前或切换后的完整文件。
func applyRestoreSqlite(target RestoreTarget, reader *innerArchiveReader, progress restoreProgress) error {
	directory := filepath.Dir(target.SQLitePath)
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("创建数据库目录: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".restore-*.sqlite")
	if err != nil {
		return fmt.Errorf("创建临时数据库: %w", err)
	}
	temporaryPath := temporary.Name()
	temporary.Close()
	switched := false
	defer func() {
		if !switched {
			removeSqliteFiles(temporaryPath)
		}
	}()

	connection, err := openSqliteDatabase(temporaryPath)
	if err != nil {
		return err
	}
	if err := createRestoreSchema(connection); err != nil {
		closeSqliteDatabase(connection)
		return err
	}
	if err := importArtifactTables(connection, reader, progress); err != nil {
		closeSqliteDatabase(connection)
		return err
	}
	if err := invalidateUserSessions(connection); err != nil {
		closeSqliteDatabase(connection)
		return err
	}
	// 切换前必须让临时数据库自身完整：WAL 合并回主文件后才不依赖侧车文件。
	if err := connection.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
		helpers.AppLogger.Warnf("临时数据库 WAL 检查点失败：%v", err)
	}
	if err := verifySqliteIntegrity(connection); err != nil {
		closeSqliteDatabase(connection)
		return err
	}
	if err := closeSqliteDatabase(connection); err != nil {
		return err
	}

	// 目标就是当前连接时，必须先关闭它：仍持有旧文件句柄会让切换后的进程继续读写被替换的文件。
	if targetUsesCurrentConnection(target) {
		closeCurrentDatabase()
	}
	if err := replaceSqliteDatabase(temporaryPath, target.SQLitePath); err != nil {
		return err
	}
	switched = true
	if err := os.Chmod(target.SQLitePath, 0o600); err != nil {
		helpers.AppLogger.Warnf("设置数据库文件权限失败：%v", err)
	}
	return syncDirectory(directory)
}

// replaceSqliteDatabase 原子替换 SQLite 主文件后清理旧侧车文件。
// 不能在 Rename 前删除目标：替换失败时旧库仍必须完整可用。
func replaceSqliteDatabase(temporaryPath string, targetPath string) error {
	if err := os.Rename(temporaryPath, targetPath); err != nil {
		return fmt.Errorf("切换数据库文件: %w", err)
	}
	if err := removeSqliteSidecars(targetPath); err != nil {
		return err
	}
	return nil
}

// applyRestorePostgres 在目标库的单一事务中清空、导入、失效会话并修复序列。
// 任一错误回滚整个事务，因此目标库不会停留在只导入了一部分表的状态。
func applyRestorePostgres(target RestoreTarget, reader *innerArchiveReader, progress restoreProgress) error {
	connection, err := openPostgresTarget(target)
	if err != nil {
		return err
	}
	defer closePostgresTarget(connection)

	// 目标库缺表时先补齐结构：恢复目标可能是一个尚未初始化的数据库。
	// 已初始化的目标不会被这里改动，真正的 schema 升级仍由下一次启动的既有迁移链完成。
	if err := ensureRestoreSchema(connection); err != nil {
		return err
	}
	catalog := models.RegularBackupRestoreTableCatalog()
	return connection.Transaction(func(transaction *gorm.DB) error {
		if err := clearCatalogTables(transaction, catalog); err != nil {
			return err
		}
		if err := importArtifactTables(transaction, reader, progress); err != nil {
			return err
		}
		if err := invalidateUserSessions(transaction); err != nil {
			return err
		}
		return repairSequences(transaction, catalog)
	})
}

// createRestoreSchema 在空的临时数据库中按当前模型建表。
// 工件可能来自较低 schema 版本，其数据导入后由下一次启动的既有迁移链升级。
func createRestoreSchema(connection *gorm.DB) error {
	if err := connection.AutoMigrate(models.AllTables...); err != nil {
		return fmt.Errorf("创建数据库结构: %w", err)
	}
	return nil
}

// verifySqliteIntegrity 读取 PRAGMA integrity_check 的实际结果。
// 只看语句是否报错并不构成校验：损坏的数据库同样会返回一行说明文本而不是错误。
func verifySqliteIntegrity(connection *gorm.DB) error {
	var result string
	if err := connection.Raw("PRAGMA integrity_check").Scan(&result).Error; err != nil {
		return fmt.Errorf("临时数据库完整性校验失败: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(result), "ok") {
		return fmt.Errorf("%w：临时数据库完整性校验未通过", ErrInvalidArtifact)
	}
	return nil
}

func ensureRestoreSchema(connection *gorm.DB) error {
	for _, entry := range models.MasterTableCatalog() {
		if !connection.Migrator().HasTable(entry.Model) {
			return createRestoreSchema(connection)
		}
	}
	return nil
}

// importArtifactTables 按主表目录的导入顺序导入工件中的全部数据表。
func importArtifactTables(database *gorm.DB, reader *innerArchiveReader, progress restoreProgress) error {
	catalog := models.RegularBackupRestoreTableCatalog()
	for index, entry := range catalog {
		archivePath := "data/" + entry.ID + ".jsonl"
		var imported int64
		err := reader.WithFile(archivePath, func(source io.Reader) error {
			count, err := importCatalogJSONL(database, entry, source)
			imported = count
			return err
		})
		if err != nil {
			return fmt.Errorf("导入表 %s: %w", entry.ID, err)
		}
		if progress != nil {
			progress(index+1, len(catalog), fmt.Sprintf("已导入 %s，共 %d 条", entry.PhysicalName, imported))
		}
	}
	return nil
}

// mirrorWhitelistFiles 按清单精确镜像白名单文件树。
// logs 为 true 时处理 config/logs/ 下的日志，否则处理配置与 TLS 证书/私钥。
// 「精确」包含删除：目标中存在而工件中没有的同类文件必须消失，否则会残留恢复前的配置或日志。
func mirrorWhitelistFiles(reader *innerArchiveReader, logs bool) error {
	expected := make(map[string]struct{})
	staged := make(map[string]string)
	stagingDir, err := os.MkdirTemp(RestoreWorkDir(), "switch-*")
	if err != nil {
		return fmt.Errorf("创建切换暂存目录: %w", err)
	}
	defer os.RemoveAll(stagingDir)

	for _, archivePath := range reader.ConfigPaths() {
		if isArtifactLogPath(archivePath) != logs {
			continue
		}
		if !isAllowedConfigPath(archivePath) {
			return fmt.Errorf("%w：清单包含未授权文件", ErrInvalidArtifact)
		}
		expected[archivePath] = struct{}{}
		// 先整体预备并校验，再逐个 rename 切换：校验失败时目标文件树尚未被改动。
		// 保留工件内的目录层级，避免 config/logs/a_b 与
		// config/logs/a/b 等不同路径映射到同一个暂存文件。
		stagedPath := filepath.Join(stagingDir, filepath.FromSlash(archivePath))
		if !isPathInsideDir(stagedPath, stagingDir) {
			return fmt.Errorf("%w：切换暂存路径无效", ErrInvalidArtifact)
		}
		if err := os.MkdirAll(filepath.Dir(stagedPath), 0o700); err != nil {
			return fmt.Errorf("创建切换暂存目录: %w", err)
		}
		file, err := os.OpenFile(stagedPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("创建切换暂存文件: %w", err)
		}
		copyErr := reader.CopyTo(archivePath, file)
		syncErr := file.Sync()
		file.Close()
		if copyErr != nil {
			return copyErr
		}
		if syncErr != nil {
			return fmt.Errorf("同步切换暂存文件: %w", syncErr)
		}
		staged[archivePath] = stagedPath
	}

	for archivePath, stagedPath := range staged {
		destination := artifactSourcePath(helpers.ConfigDir, archivePath)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("创建配置目录: %w", err)
		}
		if err := copyFileAtomically(stagedPath, destination); err != nil {
			return fmt.Errorf("切换白名单文件: %w", err)
		}
	}
	return removeWhitelistFilesOutsideSet(expected, func(archivePath string) bool {
		return isArtifactLogPath(archivePath) == logs
	})
}

// removeWhitelistFilesOutsideSet 删除 include 选中的白名单文件中不在 expected 集合内的目标文件。
// include 为 nil 时检查完整白名单文件树。
func removeWhitelistFilesOutsideSet(expected map[string]struct{}, include func(string) bool) error {
	current, err := CollectArtifactConfigSources(helpers.ConfigDir)
	if err != nil {
		return fmt.Errorf("收集白名单文件: %w", err)
	}
	for _, source := range current {
		if include != nil && !include(source.ArchivePath) {
			continue
		}
		if _, keep := expected[source.ArchivePath]; keep {
			continue
		}
		if err := os.Remove(source.SourcePath); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("删除多余白名单文件: %w", err)
		}
	}
	return nil
}

func isArtifactLogPath(archivePath string) bool {
	return strings.HasPrefix(archivePath, "config/logs/")
}

// closeCurrentDatabase 关闭当前进程的数据库连接。
// 恢复提交后进程必须有序退出，因此这里不再重连，也不允许后续业务写入。
func closeCurrentDatabase() {
	if db.Db == nil {
		return
	}
	sqlDB, err := db.Db.DB()
	if err != nil {
		return
	}
	if err := sqlDB.Close(); err != nil {
		helpers.AppLogger.Warnf("关闭当前数据库连接失败：%v", err)
	}
}
