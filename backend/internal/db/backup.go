package db

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// OpenBackupReader 为文件 SQLite 创建独立只读连接，避免一致快照占用业务池的唯一连接。
// PostgreSQL 和没有独立文件的内存 SQLite 继续使用原池；调用方必须调用返回的关闭函数。
func OpenBackupReader(database *gorm.DB) (*gorm.DB, func() error, error) {
	unchanged := func() error { return nil }
	if database.Dialector.Name() != "sqlite" {
		return database, unchanged, nil
	}
	var databases []struct{ Name, File string }
	if err := database.Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
		return nil, nil, fmt.Errorf("读取 SQLite 备份路径失败：%w", err)
	}
	var path string
	for _, entry := range databases {
		if entry.Name == "main" {
			path = entry.File
		}
	}
	if path == "" {
		return database, unchanged, nil
	}
	pool, err := database.DB()
	if err != nil {
		return nil, nil, err
	}
	// 与业务池共用门禁，使恢复排空也涵盖专用备份事务。
	gate := &maintenanceGate{changed: make(chan struct{})}
	if value, exists := maintenancePools.Load(pool); exists {
		gate = value.(*maintenanceGate)
	}
	dsn := url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	dsn.RawQuery = url.Values{"mode": {"ro"}, "_pragma": {"busy_timeout(10000)", "query_only(1)"}}.Encode()
	readerPool := sql.OpenDB(&maintenanceConnector{base: pool.Driver(), dsn: dsn.String(), gate: gate})
	readerPool.SetMaxOpenConns(1)
	readerPool.SetMaxIdleConns(1)
	reader, err := gorm.Open(sqlite.Dialector{Conn: readerPool}, &gorm.Config{
		SkipDefaultTransaction: true,
		Logger:                 database.Logger,
		NamingStrategy:         database.NamingStrategy,
	})
	if err != nil {
		return nil, nil, errors.Join(err, readerPool.Close())
	}
	return reader.WithContext(database.Statement.Context), readerPool.Close, nil
}
