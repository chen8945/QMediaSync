package backup

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"
	"qmediasync/internal/models"

	"gorm.io/gorm"
)

const (
	snapshotMetaFileName       = "snapshot.json"
	snapshotTargetConfigName   = "target-config.yaml"
	snapshotDatabaseDirName    = "database"
	snapshotConfigDirName      = "config"
	snapshotSqliteFileName     = "database.sqlite"
	snapshotMetaMaxSize        = 1 << 20
	snapshotTargetConfigMaxCap = artifactMaxManifestSize
)

// ErrRestoreSnapshotFailed 表示预恢复快照无法创建或无法回滚。
// 快照不可用时绝不能继续覆盖数据：没有快照就没有自动回滚能力。
var ErrRestoreSnapshotFailed = errors.New("预恢复快照失败")

// restoreSnapshotMeta 是快照的非敏感描述。
// 目标连接凭据不写入这里：回滚需要的连接参数从快照保存的目标配置副本解析。
type restoreSnapshotMeta struct {
	OperationID    string   `json:"operation_id"`
	Engine         string   `json:"engine"`
	TargetLabel    string   `json:"target_label"`
	SQLiteExisted  bool     `json:"sqlite_existed"`
	SQLiteSidecars []string `json:"sqlite_sidecars,omitempty"`
	PostgresTables []string `json:"postgres_tables,omitempty"`
	ConfigFiles    []string `json:"config_files"`
	CreatedAt      int64    `json:"created_at"`
}

// RestoreSnapshot 是恢复目标数据库与白名单文件树的可回滚副本。
// 它不进入备份列表、不受保留清理影响，只由新进程在终态确认后删除。
type RestoreSnapshot struct {
	dir    string
	meta   restoreSnapshotMeta
	target RestoreTarget
}

// CreateRestoreSnapshot 在覆盖任何数据前创建预恢复快照。
// targetConfigYAML 是工件携带的配置副本：回滚必须能在新进程中重新解析出同一个恢复目标。
func CreateRestoreSnapshot(operationID string, target RestoreTarget, targetConfigYAML []byte) (*RestoreSnapshot, error) {
	if !isOperationID(operationID) {
		return nil, fmt.Errorf("%w：操作标识无效", ErrRestoreSnapshotFailed)
	}
	directory := snapshotDir(operationID)
	if err := os.RemoveAll(directory); err != nil {
		return nil, fmt.Errorf("%w：清理旧快照失败", ErrRestoreSnapshotFailed)
	}
	if err := os.MkdirAll(filepath.Join(directory, snapshotDatabaseDirName), 0o700); err != nil {
		return nil, fmt.Errorf("%w：创建快照目录失败", ErrRestoreSnapshotFailed)
	}

	snapshot := &RestoreSnapshot{
		dir:    directory,
		target: target,
		meta: restoreSnapshotMeta{
			OperationID: operationID,
			Engine:      target.Engine,
			TargetLabel: target.Label,
			CreatedAt:   time.Now().Unix(),
		},
	}
	if err := os.WriteFile(filepath.Join(directory, snapshotTargetConfigName), targetConfigYAML, 0o600); err != nil {
		return nil, fmt.Errorf("%w：保存目标配置副本失败", ErrRestoreSnapshotFailed)
	}
	if err := snapshot.captureDatabase(); err != nil {
		return nil, err
	}
	if err := snapshot.captureConfigTree(); err != nil {
		return nil, err
	}
	if err := snapshot.writeMeta(); err != nil {
		return nil, err
	}
	return snapshot, nil
}

// LoadRestoreSnapshot 读取上一次进程留下的快照，供启动期幂等回滚使用。
func LoadRestoreSnapshot(operationID string) (*RestoreSnapshot, error) {
	directory := snapshotDir(operationID)
	data, err := os.ReadFile(filepath.Join(directory, snapshotMetaFileName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w：读取快照描述失败", ErrRestoreSnapshotFailed)
	}
	if len(data) > snapshotMetaMaxSize {
		return nil, fmt.Errorf("%w：快照描述超出限制", ErrRestoreSnapshotFailed)
	}
	var meta restoreSnapshotMeta
	if err := decodeStrictJSON(data, &meta); err != nil || meta.OperationID != operationID {
		return nil, fmt.Errorf("%w：快照描述无效", ErrRestoreSnapshotFailed)
	}

	targetConfig, err := os.ReadFile(filepath.Join(directory, snapshotTargetConfigName))
	if err != nil || len(targetConfig) > snapshotTargetConfigMaxCap {
		return nil, fmt.Errorf("%w：快照缺少目标配置副本", ErrRestoreSnapshotFailed)
	}
	target, err := resolveRestoreTarget(targetConfig)
	if err != nil {
		return nil, fmt.Errorf("%w：快照目标无法解析", ErrRestoreSnapshotFailed)
	}
	return &RestoreSnapshot{dir: directory, meta: meta, target: target}, nil
}

// Rollback 幂等地恢复快照：先恢复目标数据库，再恢复白名单配置和日志文件树。
// 它可以在同一次恢复失败后立即执行，也可以在下一次进程启动时重复执行。
func (snapshot *RestoreSnapshot) Rollback() error {
	if err := snapshot.rollbackDatabase(); err != nil {
		return err
	}
	return snapshot.rollbackConfigTree()
}

// Remove 删除快照目录。只有新进程确认终态后才可调用。
func (snapshot *RestoreSnapshot) Remove() error {
	if err := os.RemoveAll(snapshot.dir); err != nil {
		return fmt.Errorf("删除预恢复快照: %w", err)
	}
	return nil
}

func (snapshot *RestoreSnapshot) captureDatabase() error {
	switch snapshot.target.Engine {
	case string(helpers.DbEngineSqlite):
		return snapshot.captureSqlite()
	case string(helpers.DbEnginePostgres):
		return snapshot.capturePostgres()
	default:
		return fmt.Errorf("%w：目标数据库引擎无效", ErrRestoreSnapshotFailed)
	}
}

// captureSqlite 保留恢复目标的数据库文件及其 WAL 侧车文件。
// 目标就是当前连接时先做一次 WAL 检查点，使复制出的文件自身完整。
func (snapshot *RestoreSnapshot) captureSqlite() error {
	if targetUsesCurrentConnection(snapshot.target) {
		if err := db.Db.Exec("PRAGMA wal_checkpoint(TRUNCATE)").Error; err != nil {
			helpers.AppLogger.Warnf("恢复前 WAL 检查点失败，将按现状复制数据库文件")
		}
	}
	info, err := os.Lstat(snapshot.target.SQLitePath)
	if os.IsNotExist(err) {
		// 目标文件尚不存在时，回滚意味着把它删除，因此不复制任何数据库文件。
		snapshot.meta.SQLiteExisted = false
		return nil
	}
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("%w：SQLite 目标不可读", ErrRestoreSnapshotFailed)
	}
	snapshot.meta.SQLiteExisted = true
	if err := copyFile(snapshot.target.SQLitePath, filepath.Join(snapshot.databaseDir(), snapshotSqliteFileName)); err != nil {
		return fmt.Errorf("%w：复制 SQLite 数据库失败", ErrRestoreSnapshotFailed)
	}
	for _, suffix := range sqliteSidecarSuffixes() {
		sidecar := snapshot.target.SQLitePath + suffix
		if _, err := os.Lstat(sidecar); err != nil {
			continue
		}
		if err := copyFile(sidecar, filepath.Join(snapshot.databaseDir(), snapshotSqliteFileName+suffix)); err != nil {
			return fmt.Errorf("%w：复制 SQLite 侧车文件失败", ErrRestoreSnapshotFailed)
		}
		snapshot.meta.SQLiteSidecars = append(snapshot.meta.SQLiteSidecars, suffix)
	}
	return nil
}

// capturePostgres 以受限 JSON Lines 导出目标库的一致读视图。
// 发行物不能依赖 PostgreSQL 客户端工具，因此快照使用与备份工件相同的导出机制。
func (snapshot *RestoreSnapshot) capturePostgres() error {
	connection, err := openPostgresTarget(snapshot.target)
	if err != nil {
		return fmt.Errorf("%w：目标数据库不可连接", ErrRestoreSnapshotFailed)
	}
	defer closePostgresTarget(connection)

	transaction := connection.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if transaction.Error != nil {
		return fmt.Errorf("%w：目标数据库无法创建一致读视图", ErrRestoreSnapshotFailed)
	}
	defer transaction.Rollback()

	for _, entry := range models.RegularBackupRestoreTableCatalog() {
		if !transaction.Migrator().HasTable(entry.Model) {
			continue
		}
		destination := filepath.Join(snapshot.databaseDir(), entry.ID+".jsonl")
		if _, err := WriteArtifactJSONL(destination, func(writer *JSONLWriter) error {
			return streamTableRecords(transaction, entry.Model, writer)
		}); err != nil {
			return fmt.Errorf("%w：导出目标数据失败", ErrRestoreSnapshotFailed)
		}
		snapshot.meta.PostgresTables = append(snapshot.meta.PostgresTables, entry.ID)
	}
	if err := transaction.Commit().Error; err != nil {
		return fmt.Errorf("%w：结束一致读事务失败", ErrRestoreSnapshotFailed)
	}
	return nil
}

// captureConfigTree 复制当前完整白名单文件树，使配置、证书、私钥和日志都可回滚。
func (snapshot *RestoreSnapshot) captureConfigTree() error {
	sources, err := CollectArtifactConfigSources(helpers.ConfigDir)
	if err != nil {
		return fmt.Errorf("%w：收集白名单文件失败", ErrRestoreSnapshotFailed)
	}
	for _, source := range sources {
		destination := snapshot.configFilePath(source.ArchivePath)
		if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
			return fmt.Errorf("%w：创建快照配置目录失败", ErrRestoreSnapshotFailed)
		}
		if err := copyFile(source.SourcePath, destination); err != nil {
			return fmt.Errorf("%w：复制白名单文件失败", ErrRestoreSnapshotFailed)
		}
		snapshot.meta.ConfigFiles = append(snapshot.meta.ConfigFiles, source.ArchivePath)
	}
	return nil
}

func (snapshot *RestoreSnapshot) rollbackDatabase() error {
	switch snapshot.meta.Engine {
	case string(helpers.DbEngineSqlite):
		return snapshot.rollbackSqlite()
	case string(helpers.DbEnginePostgres):
		return snapshot.rollbackPostgres()
	default:
		return fmt.Errorf("%w：快照引擎无效", ErrRestoreSnapshotFailed)
	}
}

func (snapshot *RestoreSnapshot) rollbackSqlite() error {
	targetPath := snapshot.target.SQLitePath
	if !snapshot.meta.SQLiteExisted {
		// 快照时目标不存在：回滚就是移除恢复过程创建的数据库及其侧车文件。
		return removeSqliteFiles(targetPath)
	}
	source := filepath.Join(snapshot.databaseDir(), snapshotSqliteFileName)
	if _, err := os.Lstat(source); err != nil {
		return fmt.Errorf("%w：快照缺少 SQLite 数据库", ErrRestoreSnapshotFailed)
	}
	if err := copyFileAtomically(source, targetPath); err != nil {
		return fmt.Errorf("%w：还原 SQLite 数据库失败", ErrRestoreSnapshotFailed)
	}
	if err := removeSqliteSidecars(targetPath); err != nil {
		return err
	}
	for _, suffix := range snapshot.meta.SQLiteSidecars {
		sidecarSource := filepath.Join(snapshot.databaseDir(), snapshotSqliteFileName+suffix)
		if _, err := os.Lstat(sidecarSource); err != nil {
			continue
		}
		if err := copyFileAtomically(sidecarSource, targetPath+suffix); err != nil {
			return fmt.Errorf("%w：还原 SQLite 侧车文件失败", ErrRestoreSnapshotFailed)
		}
	}
	return nil
}

func (snapshot *RestoreSnapshot) rollbackPostgres() error {
	connection, err := openPostgresTarget(snapshot.target)
	if err != nil {
		return fmt.Errorf("%w：目标数据库不可连接", ErrRestoreSnapshotFailed)
	}
	defer closePostgresTarget(connection)

	catalog := models.RegularBackupRestoreTableCatalog()
	err = connection.Transaction(func(transaction *gorm.DB) error {
		if err := clearCatalogTables(transaction, catalog); err != nil {
			return err
		}
		for _, entry := range catalog {
			source := filepath.Join(snapshot.databaseDir(), entry.ID+".jsonl")
			file, openErr := os.Open(source)
			if os.IsNotExist(openErr) {
				continue
			}
			if openErr != nil {
				return fmt.Errorf("打开快照数据失败: %w", openErr)
			}
			_, importErr := importCatalogJSONL(transaction, entry, file)
			file.Close()
			if importErr != nil {
				return importErr
			}
		}
		return repairSequences(transaction, catalog)
	})
	if err != nil {
		return fmt.Errorf("%w：还原目标数据失败", ErrRestoreSnapshotFailed)
	}
	return nil
}

// rollbackConfigTree 精确还原白名单文件树：写回快照内容，并删除快照中不存在的同类文件。
func (snapshot *RestoreSnapshot) rollbackConfigTree() error {
	expected := make(map[string]struct{}, len(snapshot.meta.ConfigFiles))
	for _, archivePath := range snapshot.meta.ConfigFiles {
		if !isAllowedConfigPath(archivePath) {
			return fmt.Errorf("%w：快照包含未授权文件", ErrRestoreSnapshotFailed)
		}
		expected[archivePath] = struct{}{}
		source := snapshot.configFilePath(archivePath)
		destination := artifactSourcePath(helpers.ConfigDir, archivePath)
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return fmt.Errorf("%w：创建配置目录失败", ErrRestoreSnapshotFailed)
		}
		if err := copyFileAtomically(source, destination); err != nil {
			return fmt.Errorf("%w：还原白名单文件失败", ErrRestoreSnapshotFailed)
		}
	}
	if err := removeWhitelistFilesOutsideSet(expected, nil); err != nil {
		return fmt.Errorf("%w：%w", ErrRestoreSnapshotFailed, err)
	}
	return nil
}

func (snapshot *RestoreSnapshot) writeMeta() error {
	data, err := json.Marshal(snapshot.meta)
	if err != nil {
		return fmt.Errorf("%w：编码快照描述失败", ErrRestoreSnapshotFailed)
	}
	if err := replaceFileAtomically(filepath.Join(snapshot.dir, snapshotMetaFileName), func(output *os.File) error {
		if _, err := output.Write(data); err != nil {
			return fmt.Errorf("写入快照描述: %w", err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("%w：保存快照描述失败", ErrRestoreSnapshotFailed)
	}
	return syncDirectory(snapshot.dir)
}

func (snapshot *RestoreSnapshot) databaseDir() string {
	return filepath.Join(snapshot.dir, snapshotDatabaseDirName)
}

func (snapshot *RestoreSnapshot) configFilePath(archivePath string) string {
	relative := filepath.FromSlash(strings.TrimPrefix(archivePath, "config/"))
	return filepath.Join(snapshot.dir, snapshotConfigDirName, relative)
}

func snapshotDir(operationID string) string {
	return filepath.Join(RollbackDir(), operationID)
}

// RemoveOtherSnapshots 删除除 keepOperationID 之外的历史快照。
// 只保留最近一次操作的快照，与「终态和阶段日志只保留最近一次」的语义一致。
func RemoveOtherSnapshots(keepOperationID string) {
	entries, err := os.ReadDir(RollbackDir())
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		helpers.AppLogger.Warnf("读取快照目录失败：%v", err)
		return
	}
	for _, entry := range entries {
		if entry.Name() == keepOperationID {
			continue
		}
		if err := os.RemoveAll(filepath.Join(RollbackDir(), entry.Name())); err != nil {
			helpers.AppLogger.Warnf("清理历史快照失败：%v", err)
		}
	}
}

func sqliteSidecarSuffixes() []string {
	return []string{"-wal", "-shm", "-journal"}
}

func removeSqliteFiles(targetPath string) error {
	if err := os.Remove(targetPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("%w：清理 SQLite 目标失败", ErrRestoreSnapshotFailed)
	}
	return removeSqliteSidecars(targetPath)
}

func removeSqliteSidecars(targetPath string) error {
	for _, path := range sqliteSidecarPaths(targetPath) {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("%w：清理 SQLite 侧车文件失败", ErrRestoreSnapshotFailed)
		}
	}
	return nil
}

func sqliteSidecarPaths(targetPath string) []string {
	suffixes := sqliteSidecarSuffixes()
	paths := make([]string, 0, len(suffixes))
	for _, suffix := range suffixes {
		paths = append(paths, targetPath+suffix)
	}
	return paths
}

func copyFile(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("打开源文件: %w", err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("创建目标目录: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("创建目标文件: %w", err)
	}
	defer output.Close()
	if _, err := io.Copy(output, input); err != nil {
		return fmt.Errorf("复制文件内容: %w", err)
	}
	if err := output.Sync(); err != nil {
		return fmt.Errorf("同步目标文件: %w", err)
	}
	return nil
}

// copyFileAtomically 以临时文件加 rename 的方式替换目标，使中断只能留下替换前或替换后的完整文件。
func copyFileAtomically(source string, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("打开源文件: %w", err)
	}
	defer input.Close()
	return replaceFileAtomically(destination, func(output *os.File) error {
		if _, err := io.Copy(output, input); err != nil {
			return fmt.Errorf("复制文件内容: %w", err)
		}
		return nil
	})
}
