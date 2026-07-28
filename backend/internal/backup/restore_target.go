package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"qmediasync/internal/db"
	"qmediasync/internal/helpers"

	"github.com/shirou/gopsutil/v4/disk"
	"gopkg.in/yaml.v2"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/glebarez/sqlite"
)

// restoreTargetProbeTimeout 是预检连接恢复目标数据库的等待上限。
// 它只约束「目标是否可连接」这一次探测，不是恢复提交或自动回滚的执行时限。
const restoreTargetProbeTimeout = 10 * time.Second

// restoreSpaceMargin 是空间预检在数据量之外额外要求的余量。
// 恢复过程需要同时存放解密后的内层归档、预恢复快照和临时数据库。
const restoreSpaceMargin = int64(64 << 20)

var (
	// ErrRestoreTargetIncompatible 表示备份配置指定的目标与工件引擎或版本不兼容。
	ErrRestoreTargetIncompatible = errors.New("备份与目标数据库不兼容")
	// ErrRestoreTargetUnavailable 表示目标数据库不可连接或不可创建快照。
	ErrRestoreTargetUnavailable = errors.New("目标数据库不可用")
	// ErrRestoreInsufficientSpace 表示可用磁盘空间不足以完成恢复。
	ErrRestoreInsufficientSpace = errors.New("磁盘可用空间不足")
)

// RestorePostgresTarget 是恢复目标的 PostgreSQL 连接参数。
// Password 只在进程内用于建立连接，绝不进入标识、状态、响应或日志。
type RestorePostgresTarget struct {
	Host     string
	Port     int
	User     string
	Password string
	DBName   string
	SSLMode  string
}

// RestoreTarget 是备份工件所携带配置指定的恢复目标数据库。
// 恢复始终写入该目标；它与当前进程连接的数据库不同时，当前数据库保持只读且不进入写入范围。
type RestoreTarget struct {
	Engine        string
	Label         string
	SQLitePath    string
	Postgres      RestorePostgresTarget
	SameAsCurrent bool
}

// resolveRestoreTarget 从工件内的 config.yaml 解析恢复目标。
// 恢复目标由备份携带的配置决定，因此恢复可能改变重启后的数据库连接目标；
// 恢复界面必须在启动前展示这里生成的不含密码标识。
func resolveRestoreTarget(configYAML []byte) (RestoreTarget, error) {
	var config helpers.Config
	if err := yaml.Unmarshal(configYAML, &config); err != nil {
		return RestoreTarget{}, fmt.Errorf("%w：备份配置无法解析", ErrRestoreTargetIncompatible)
	}

	switch config.Db.Engine {
	case helpers.DbEngineSqlite:
		return resolveSqliteRestoreTarget(config)
	case helpers.DbEnginePostgres:
		return resolvePostgresRestoreTarget(config)
	default:
		return RestoreTarget{}, fmt.Errorf("%w：备份配置未指定受支持的数据库引擎", ErrRestoreTargetIncompatible)
	}
}

func resolveSqliteRestoreTarget(config helpers.Config) (RestoreTarget, error) {
	relative := strings.TrimSpace(config.Db.SqliteFile)
	if relative == "" {
		return RestoreTarget{}, fmt.Errorf("%w：备份配置缺少 SQLite 文件", ErrRestoreTargetIncompatible)
	}
	// SQLite 目标始终相对本机配置目录解析；绝对路径或向上逃逸会让恢复写到配置目录之外。
	if filepath.IsAbs(relative) || strings.Contains(filepath.ToSlash(relative), "../") {
		return RestoreTarget{}, fmt.Errorf("%w：SQLite 文件路径无效", ErrRestoreTargetIncompatible)
	}
	absolute := filepath.Join(helpers.ConfigDir, filepath.FromSlash(relative))
	if !isPathInsideDir(absolute, helpers.ConfigDir) {
		return RestoreTarget{}, fmt.Errorf("%w：SQLite 文件路径无效", ErrRestoreTargetIncompatible)
	}
	if !isPathInsideResolvedDir(absolute, helpers.ConfigDir) {
		return RestoreTarget{}, fmt.Errorf("%w：SQLite 文件路径无效", ErrRestoreTargetIncompatible)
	}

	target := RestoreTarget{
		Engine:     string(helpers.DbEngineSqlite),
		Label:      "sqlite:" + filepath.ToSlash(relative),
		SQLitePath: absolute,
	}
	target.SameAsCurrent = helpers.GlobalConfig.Db.Engine == helpers.DbEngineSqlite &&
		filepath.Clean(currentSqliteTargetPath()) == filepath.Clean(absolute)
	return target, nil
}

func resolvePostgresRestoreTarget(config helpers.Config) (RestoreTarget, error) {
	settings := config.Db.PostgresConfig
	if settings.Host == "" || settings.Port <= 0 || settings.User == "" || settings.Database == "" {
		return RestoreTarget{}, fmt.Errorf("%w：备份配置缺少 PostgreSQL 连接参数", ErrRestoreTargetIncompatible)
	}
	postgresType := config.Db.PostgresType
	if postgresType != helpers.PostgresTypeEmbedded && postgresType != helpers.PostgresTypeExternal {
		return RestoreTarget{}, fmt.Errorf("%w：备份配置的 PostgreSQL 类型无效", ErrRestoreTargetIncompatible)
	}
	sslMode := "disable"
	if settings.SSL {
		sslMode = "require"
	}

	target := RestoreTarget{
		Engine: string(helpers.DbEnginePostgres),
		Label: fmt.Sprintf(
			"postgres(%s) %s:%d/%s",
			postgresType,
			settings.Host,
			settings.Port,
			settings.Database,
		),
		Postgres: RestorePostgresTarget{
			Host:     settings.Host,
			Port:     settings.Port,
			User:     settings.User,
			Password: settings.Password,
			DBName:   settings.Database,
			SSLMode:  sslMode,
		},
	}
	current := helpers.GlobalConfig.Db.PostgresConfig
	target.SameAsCurrent = helpers.GlobalConfig.Db.Engine == helpers.DbEnginePostgres &&
		current.Host == settings.Host && current.Port == settings.Port && current.Database == settings.Database
	return target, nil
}

// currentSqliteTargetPath 返回当前进程连接的 SQLite 文件路径。
func currentSqliteTargetPath() string {
	relative := strings.TrimSpace(helpers.GlobalConfig.Db.SqliteFile)
	if relative == "" {
		return ""
	}
	return filepath.Join(helpers.ConfigDir, filepath.FromSlash(relative))
}

// verifyRestoreCompatibility 校验工件与恢复目标的引擎、schema 版本是否可恢复。
// 高版本 schema 一律拒绝；低版本工件导入后由既有迁移链在下次启动升级。
func verifyRestoreCompatibility(header ArtifactHeader, target RestoreTarget, currentSchemaVersion int) error {
	if header.SourceEngine != target.Engine {
		return fmt.Errorf("%w：工件与目标数据库引擎不一致", ErrRestoreTargetIncompatible)
	}
	if header.SchemaVersion > currentSchemaVersion {
		return fmt.Errorf("%w：工件来自更高版本", ErrRestoreTargetIncompatible)
	}
	return nil
}

// checkRestoreTargetReady 验证目标数据库可连接、可创建快照，并且磁盘空间足够。
// 它是只读探测：不创建数据库、不建表、不写入任何业务数据。
func checkRestoreTargetReady(target RestoreTarget, payloadPlaintextSize int64) error {
	switch target.Engine {
	case string(helpers.DbEngineSqlite):
		return checkSqliteTargetReady(target, payloadPlaintextSize)
	case string(helpers.DbEnginePostgres):
		return checkPostgresTargetReady(target, payloadPlaintextSize)
	default:
		return fmt.Errorf("%w：目标数据库引擎无效", ErrRestoreTargetIncompatible)
	}
}

func checkSqliteTargetReady(target RestoreTarget, payloadPlaintextSize int64) error {
	directory := filepath.Dir(target.SQLitePath)
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("%w：SQLite 目标目录不可用", ErrRestoreTargetUnavailable)
	}
	// 临时文件探测同时证明两件事：目录可写，且临时数据库能与目标处于同一文件系统，
	// 因此提交阶段的原子切换可用。
	probe, err := os.CreateTemp(directory, ".restore-probe-*")
	if err != nil {
		return fmt.Errorf("%w：SQLite 目标目录不可写", ErrRestoreTargetUnavailable)
	}
	probePath := probe.Name()
	probe.Close()
	if err := os.Remove(probePath); err != nil {
		return fmt.Errorf("%w：SQLite 目标目录不可写", ErrRestoreTargetUnavailable)
	}

	var currentSize int64
	if targetInfo, err := os.Lstat(target.SQLitePath); err == nil {
		if !targetInfo.Mode().IsRegular() {
			return fmt.Errorf("%w：SQLite 目标不是常规文件", ErrRestoreTargetUnavailable)
		}
		currentSize = targetInfo.Size()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("%w：SQLite 目标不可读", ErrRestoreTargetUnavailable)
	}

	required := payloadPlaintextSize + currentSize + restoreSpaceMargin
	if err := checkFreeSpace(directory, required); err != nil {
		return err
	}
	return checkFreeSpace(StateDir(), currentSize+restoreSpaceMargin)
}

func checkPostgresTargetReady(target RestoreTarget, payloadPlaintextSize int64) error {
	connection, err := openPostgresTarget(target)
	if err != nil {
		return err
	}
	defer closePostgresTarget(connection)

	ctx, cancel := context.WithTimeout(context.Background(), restoreTargetProbeTimeout)
	defer cancel()
	// 快照能力即「能否在目标库开启只读一致读事务」：预恢复快照正是用它导出的。
	transaction := connection.WithContext(ctx).Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if transaction.Error != nil {
		return fmt.Errorf("%w：目标数据库无法创建一致读视图", ErrRestoreTargetUnavailable)
	}
	var probe int
	if err := transaction.Raw("SELECT 1").Scan(&probe).Error; err != nil {
		transaction.Rollback()
		return fmt.Errorf("%w：目标数据库无法读取", ErrRestoreTargetUnavailable)
	}
	var databaseSize int64
	if err := transaction.Raw("SELECT pg_database_size(current_database())").Scan(&databaseSize).Error; err != nil {
		databaseSize = 0
	}
	if err := transaction.Rollback().Error; err != nil {
		return fmt.Errorf("%w：目标数据库无法结束探测事务", ErrRestoreTargetUnavailable)
	}

	// PostgreSQL 的预恢复快照与解密后的内层归档都写在本机状态目录，因此空间检查针对它。
	return checkFreeSpace(StateDir(), payloadPlaintextSize+databaseSize+restoreSpaceMargin)
}

// openPostgresTarget 建立到恢复目标的独立连接。
// 它不复用全局连接：目标可能不是当前进程连接的数据库，当前数据库不得被写入。
func openPostgresTarget(target RestoreTarget) (*gorm.DB, error) {
	settings := target.Postgres
	dsn := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s connect_timeout=%d",
		settings.Host,
		settings.Port,
		settings.User,
		settings.Password,
		settings.DBName,
		settings.SSLMode,
		int(restoreTargetProbeTimeout/time.Second),
	)
	connection, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		// 连接串含密码，绝不能进入错误链；这里只返回稳定的目标不可用语义。
		return nil, fmt.Errorf("%w：无法连接目标数据库", ErrRestoreTargetUnavailable)
	}
	sqlDB, err := connection.DB()
	if err != nil {
		return nil, fmt.Errorf("%w：无法获取目标数据库连接", ErrRestoreTargetUnavailable)
	}
	ctx, cancel := context.WithTimeout(context.Background(), restoreTargetProbeTimeout)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("%w：目标数据库无响应", ErrRestoreTargetUnavailable)
	}
	sqlDB.SetMaxOpenConns(2)
	sqlDB.SetMaxIdleConns(1)
	return connection, nil
}

func closePostgresTarget(connection *gorm.DB) {
	if connection == nil {
		return
	}
	if sqlDB, err := connection.DB(); err == nil {
		sqlDB.Close()
	}
}

// openSqliteDatabase 打开一个独立的 SQLite 连接，用于快照读取或临时数据库导入。
func openSqliteDatabase(path string) (*gorm.DB, error) {
	connection, err := gorm.Open(
		sqlite.Open(path+"?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)"),
		&gorm.Config{Logger: logger.Discard, SkipDefaultTransaction: true},
	)
	if err != nil {
		return nil, fmt.Errorf("%w：无法打开 SQLite 数据库", ErrRestoreTargetUnavailable)
	}
	return connection, nil
}

func closeSqliteDatabase(connection *gorm.DB) error {
	if connection == nil {
		return nil
	}
	sqlDB, err := connection.DB()
	if err != nil {
		return fmt.Errorf("获取 SQLite 连接: %w", err)
	}
	if err := sqlDB.Close(); err != nil {
		return fmt.Errorf("关闭 SQLite 连接: %w", err)
	}
	return nil
}

// targetUsesCurrentConnection 判断恢复目标是否就是当前进程已连接的数据库。
func targetUsesCurrentConnection(target RestoreTarget) bool {
	return target.SameAsCurrent && db.Db != nil
}

func checkFreeSpace(path string, required int64) error {
	if required <= 0 {
		return nil
	}
	usage, err := disk.Usage(path)
	if err != nil {
		// 无法探测可用空间时不阻断恢复：真正的空间不足仍会在写入阶段失败并触发自动回滚。
		helpers.AppLogger.Warnf("读取磁盘可用空间失败，跳过空间预检")
		return nil
	}
	if usage.Free < uint64(required) {
		return fmt.Errorf("%w：可用空间不足", ErrRestoreInsufficientSpace)
	}
	return nil
}

func isPathInsideDir(target string, directory string) bool {
	relative, err := filepath.Rel(filepath.Clean(directory), filepath.Clean(target))
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// isPathInsideResolvedDir 在目标尚不存在时也检查已有路径组件的符号链接。
// 仅做词法检查会让 configDir 内的链接目录把 SQLite 恢复目标带到根目录之外。
func isPathInsideResolvedDir(target string, directory string) bool {
	resolvedDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return false
	}

	current := filepath.Clean(target)
	missing := []string{}
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for index := len(missing) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, missing[index])
			}
			return isPathInsideDir(resolved, resolvedDirectory)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false
		}

		parent := filepath.Dir(current)
		if parent == current {
			return false
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}
