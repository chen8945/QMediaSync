package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"qmediasync/internal/helpers"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OpenExisting 只连接已有数据库，不建库、迁移或启动后台保活。
func OpenExisting(ctx context.Context, configDir string, config helpers.ConfigDb) (*gorm.DB, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	var dialector gorm.Dialector
	var sqlDB *sql.DB
	var err error
	switch config.Engine {
	case helpers.DbEngineSqlite:
		path := filepath.Join(configDir, config.SqliteFile)
		info, err := os.Stat(path)
		if err != nil {
			return nil, fmt.Errorf("读取已有 SQLite 数据库失败：%w", err)
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("SQLite 数据库路径不是普通文件")
		}
		path, err = filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		dialector = sqlite.Open(existingSQLiteDSN(path))
	case helpers.DbEnginePostgres:
		pg := config.PostgresConfig
		sslMode := "disable"
		if pg.SSL {
			sslMode = "require"
		}
		dsn := url.URL{
			Scheme: "postgres", User: url.UserPassword(pg.User, pg.Password),
			Path: "/" + pg.Database,
			// host 放入查询参数，支持 Unix socket，并避免非法主机名的解析错误暴露整个 URI。
			RawQuery: url.Values{
				"host": {pg.Host}, "port": {strconv.Itoa(pg.Port)},
				"sslmode": {sslMode}, "connect_timeout": {"10"},
			}.Encode(),
		}
		sqlDB, err = sql.Open("postgres", dsn.String())
		if err != nil {
			return nil, fmt.Errorf("打开 PostgreSQL 连接失败：%w", err)
		}
		dialector = postgres.New(postgres.Config{Conn: sqlDB})
	default:
		return nil, fmt.Errorf("管理员恢复需要已配置的 SQLite 或 PostgreSQL 数据库")
	}

	conn, err := gorm.Open(dialector, &gorm.Config{
		SkipDefaultTransaction: true,
		DisableAutomaticPing:   true,
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		if sqlDB != nil {
			err = errors.Join(err, sqlDB.Close())
		}
		return nil, fmt.Errorf("连接已有数据库失败：%w", err)
	}
	sqlDB, err = conn.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	if err := sqlDB.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("连接已有数据库失败：%w", err), sqlDB.Close())
	}
	return conn, nil
}

func existingSQLiteDSN(path string) string {
	// Windows 盘符需要前导斜杠，否则会被 URI 解析为主机名。
	path = "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")
	dsn := url.URL{Scheme: "file", Path: path}
	dsn.RawQuery = url.Values{"mode": {"rw"}, "_pragma": {"busy_timeout(10000)"}}.Encode()
	return dsn.String()
}
