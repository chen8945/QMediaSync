package db

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

// runReadThenWriteTx 复现账号授权落库的访问形态：事务内先做唯一性查询，再更新目标行。
// 多连接时，读快照之后其他连接的提交会让这里的写升级返回 SQLITE_BUSY_SNAPSHOT。
func runReadThenWriteTx(db *gorm.DB) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var count int64
		if err := tx.Raw("SELECT COUNT(*) FROM account_probe WHERE name = ?", "taken").Scan(&count).Error; err != nil {
			return err
		}
		return tx.Exec("UPDATE account_probe SET name = ? WHERE id = 1", "new").Error
	})
}

// TestInitSqlite3限制连接池为单连接 验证 SQLite 连接池被限制为一个连接。
// SQLite 同一时间只允许一个写事务；多连接时事务的写升级可能直接失败，
// busy_timeout 不会重试快照冲突这种错误。
func TestInitSqlite3限制连接池为单连接(t *testing.T) {
	sqliteDb := InitSqlite3(filepath.Join(t.TempDir(), "pool.db"))

	sqlDB, err := sqliteDb.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败：%v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("最大连接数应为 1，实际为 %d", got)
	}
}

// TestInitSqlite3先读后写事务在并发写入下不返回锁错误 覆盖账号授权落库的访问形态：
// 事务内先查询唯一性再更新，同时另有 goroutine 持续写入其他表（如请求统计）。
// 单连接会把这些写入排到事务前后，事务不会在写升级时遇到快照冲突。
func TestInitSqlite3先读后写事务在并发写入下不返回锁错误(t *testing.T) {
	sqliteDb := InitSqlite3(filepath.Join(t.TempDir(), "snapshot.db"))

	sqlDB, err := sqliteDb.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败：%v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	if err := sqliteDb.Exec("CREATE TABLE account_probe (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatalf("创建账号测试表失败：%v", err)
	}
	if err := sqliteDb.Exec("CREATE TABLE stat_probe (id INTEGER PRIMARY KEY, value TEXT)").Error; err != nil {
		t.Fatalf("创建统计测试表失败：%v", err)
	}
	if err := sqliteDb.Exec("INSERT INTO account_probe (id, name) VALUES (1, 'old')").Error; err != nil {
		t.Fatalf("写入初始账号失败：%v", err)
	}

	// 后台写入者模拟请求统计的异步落库，与授权事务竞争写锁。
	stop := make(chan struct{})
	var writers sync.WaitGroup
	for i := 0; i < 4; i++ {
		writers.Add(1)
		go func() {
			defer writers.Done()
			for {
				select {
				case <-stop:
					return
				default:
					sqliteDb.Exec("INSERT INTO stat_probe (value) VALUES ('s')")
				}
			}
		}()
	}
	t.Cleanup(func() {
		close(stop)
		writers.Wait()
	})

	// 反复执行先读后写事务，任何一次返回锁错误都说明写升级遇到了快照冲突。
	for attempt := 0; attempt < 30; attempt++ {
		if err := runReadThenWriteTx(sqliteDb); err != nil {
			t.Fatalf("第 %d 次先读后写事务返回错误：%v", attempt+1, err)
		}
	}

	var name string
	if err := sqliteDb.Raw("SELECT name FROM account_probe WHERE id = 1").Scan(&name).Error; err != nil {
		t.Fatalf("读取账号失败：%v", err)
	}
	if name != "new" {
		t.Fatalf("账号名应为 new，实际为 %q", name)
	}
}

// TestInitSqlite3单连接下先读后写事务不会自我死锁 保证单连接配置不会因为事务内等待新连接而卡死。
func TestInitSqlite3单连接下先读后写事务不会自我死锁(t *testing.T) {
	sqliteDb := InitSqlite3(filepath.Join(t.TempDir(), "deadlock.db"))

	sqlDB, err := sqliteDb.DB()
	if err != nil {
		t.Fatalf("获取底层连接失败：%v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	if err := sqliteDb.Exec("CREATE TABLE account_probe (id INTEGER PRIMARY KEY, name TEXT)").Error; err != nil {
		t.Fatalf("创建账号测试表失败：%v", err)
	}
	if err := sqliteDb.Exec("INSERT INTO account_probe (id, name) VALUES (1, 'old')").Error; err != nil {
		t.Fatalf("写入初始账号失败：%v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- runReadThenWriteTx(sqliteDb)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("先读后写事务返回错误：%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("先读后写事务在单连接下超时，可能出现自我死锁")
	}
}
