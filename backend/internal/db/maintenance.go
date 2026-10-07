package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"

	"gorm.io/gorm"
)

// ErrMaintenance 表示连接已进入数据库恢复维护期。
var ErrMaintenance = errors.New("数据库正在维护，请重启应用后再试")

var maintenancePools sync.Map // *sql.DB -> *maintenanceGate

type maintenanceGate struct {
	mu      sync.Mutex
	blocked bool
	active  int
	changed chan struct{}
	owner   *Maintenance
}

type maintenanceContextKey struct{}

// Maintenance 持有数据库恢复的专用许可。普通连接在释放许可前拒绝新 SQL。
type Maintenance struct {
	gate     *maintenanceGate
	database *gorm.DB
}

// OpenMaintenanceSQL 创建带维护门禁的连接池，覆盖 GORM 和直接 database/sql 调用。
func OpenMaintenanceSQL(driverName, dsn string) (*sql.DB, error) {
	probe, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	base := probe.Driver()
	if err := probe.Close(); err != nil {
		return nil, err
	}
	gate := &maintenanceGate{changed: make(chan struct{})}
	pool := sql.OpenDB(&maintenanceConnector{base: base, dsn: dsn, gate: gate})
	maintenancePools.Store(pool, gate)
	return pool, nil
}

// BeginMaintenance 拒绝新 SQL，并等待已有语句、结果集和事务全部退出。
// 超时也保持门禁；调用方必须保持维护状态，不能在任务部分退出后重新放行。
func BeginMaintenance(ctx context.Context, database *gorm.DB) (*Maintenance, error) {
	pool, err := database.DB()
	if err != nil {
		return nil, err
	}
	value, ok := maintenancePools.Load(pool)
	if !ok {
		return nil, errors.New("数据库连接未安装维护门禁")
	}
	gate := value.(*maintenanceGate)
	gate.mu.Lock()
	if gate.blocked {
		gate.mu.Unlock()
		return nil, ErrMaintenance
	}
	maintenance := &Maintenance{gate: gate}
	gate.blocked, gate.owner = true, maintenance
	maintenance.database = database.WithContext(context.WithValue(ctx, maintenanceContextKey{}, maintenance))
	for gate.active != 0 {
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return maintenance, fmt.Errorf("等待数据库操作退出失败：%w", ctx.Err())
		}
		gate.mu.Lock()
	}
	gate.mu.Unlock()
	return maintenance, nil
}

// Database 返回带维护许可的同一连接池，SQLite 无需额外连接。
func (maintenance *Maintenance) Database() *gorm.DB { return maintenance.database }

// IsMaintenance 返回指定连接池是否已关闭普通 SQL 入口。
func IsMaintenance(database *gorm.DB) bool {
	if database == nil {
		return false
	}
	pool, err := database.DB()
	if err != nil {
		return false
	}
	value, ok := maintenancePools.Load(pool)
	if !ok {
		return false
	}
	gate := value.(*maintenanceGate)
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.blocked
}

func (gate *maintenanceGate) acquire(ctx context.Context) (func(), error) {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	owner, _ := ctx.Value(maintenanceContextKey{}).(*Maintenance)
	if gate.blocked && (owner == nil || owner != gate.owner) {
		return nil, ErrMaintenance
	}
	gate.active++
	return sync.OnceFunc(func() {
		gate.mu.Lock()
		defer gate.mu.Unlock()
		gate.active--
		close(gate.changed)
		gate.changed = make(chan struct{})
	}), nil
}

type maintenanceConnector struct {
	base driver.Driver
	dsn  string
	gate *maintenanceGate
}

func (connector *maintenanceConnector) Driver() driver.Driver { return connector.base }
func (connector *maintenanceConnector) Connect(ctx context.Context) (driver.Conn, error) {
	var conn driver.Conn
	var err error
	if contextual, ok := connector.base.(driver.DriverContext); ok {
		var base driver.Connector
		base, err = contextual.OpenConnector(connector.dsn)
		if err == nil {
			conn, err = base.Connect(ctx)
		}
	} else {
		conn, err = connector.base.Open(connector.dsn)
	}
	if err != nil {
		return nil, err
	}
	return &maintenanceConn{Conn: conn, gate: connector.gate}, nil
}

type maintenanceConn struct {
	driver.Conn
	gate *maintenanceGate
	inTx bool // database/sql 串行使用同一 driver.Conn。
}

func (conn *maintenanceConn) acquire(ctx context.Context) (func(), error) {
	if conn.inTx {
		// 已经开始的事务保留许可直到提交或回滚，避免维护等待事务、事务等待维护。
		return func() {}, nil
	}
	return conn.gate.acquire(ctx)
}

func (conn *maintenanceConn) Begin() (driver.Tx, error) {
	return conn.BeginTx(context.Background(), driver.TxOptions{})
}

func (conn *maintenanceConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	release, err := conn.gate.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var tx driver.Tx
	if beginner, ok := conn.Conn.(driver.ConnBeginTx); ok {
		tx, err = beginner.BeginTx(ctx, options)
	} else if options.Isolation != 0 || options.ReadOnly {
		err = errors.New("数据库驱动不支持指定事务选项")
	} else {
		tx, err = conn.Conn.Begin()
	}
	if err != nil {
		release()
		return nil, err
	}
	conn.inTx = true
	return &maintenanceTx{Tx: tx, finish: sync.OnceFunc(func() { conn.inTx = false; release() })}, nil
}

func (conn *maintenanceConn) Prepare(query string) (driver.Stmt, error) {
	return conn.PrepareContext(context.Background(), query)
}
func (conn *maintenanceConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	release, err := conn.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	var stmt driver.Stmt
	if preparer, ok := conn.Conn.(driver.ConnPrepareContext); ok {
		stmt, err = preparer.PrepareContext(ctx, query)
	} else {
		stmt, err = conn.Conn.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &maintenanceStmt{Stmt: stmt, conn: conn}, nil
}
func (conn *maintenanceConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	release, err := conn.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if executor, ok := conn.Conn.(driver.ExecerContext); ok {
		return executor.ExecContext(ctx, query, args)
	}
	return nil, driver.ErrSkip
}
func (conn *maintenanceConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	release, err := conn.acquire(ctx)
	if err != nil {
		return nil, err
	}
	queryer, ok := conn.Conn.(driver.QueryerContext)
	if !ok {
		release()
		return nil, driver.ErrSkip
	}
	rows, err := queryer.QueryContext(ctx, query, args)
	if err != nil {
		release()
		return nil, err
	}
	return &maintenanceRows{Rows: rows, release: release}, nil
}
func (conn *maintenanceConn) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := conn.Conn.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return driver.ErrSkip
}
func (conn *maintenanceConn) Ping(ctx context.Context) error {
	release, err := conn.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if pinger, ok := conn.Conn.(driver.Pinger); ok {
		return pinger.Ping(ctx)
	}
	return nil
}
func (conn *maintenanceConn) ResetSession(ctx context.Context) error {
	if resetter, ok := conn.Conn.(driver.SessionResetter); ok {
		return resetter.ResetSession(ctx)
	}
	return nil
}
func (conn *maintenanceConn) IsValid() bool {
	if validator, ok := conn.Conn.(driver.Validator); ok {
		return validator.IsValid()
	}
	return true
}

type maintenanceTx struct {
	driver.Tx
	finish func()
}

func (tx *maintenanceTx) Commit() error   { defer tx.finish(); return tx.Tx.Commit() }
func (tx *maintenanceTx) Rollback() error { defer tx.finish(); return tx.Tx.Rollback() }

type maintenanceStmt struct {
	driver.Stmt
	conn *maintenanceConn
}

func (stmt *maintenanceStmt) Exec(values []driver.Value) (driver.Result, error) {
	release, err := stmt.conn.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	defer release()
	return stmt.Stmt.Exec(values)
}
func (stmt *maintenanceStmt) Query(values []driver.Value) (driver.Rows, error) {
	release, err := stmt.conn.acquire(context.Background())
	if err != nil {
		return nil, err
	}
	rows, err := stmt.Stmt.Query(values)
	if err != nil {
		release()
		return nil, err
	}
	return &maintenanceRows{Rows: rows, release: release}, nil
}
func (stmt *maintenanceStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	release, err := stmt.conn.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	if executor, ok := stmt.Stmt.(driver.StmtExecContext); ok {
		return executor.ExecContext(ctx, args)
	}
	values, err := maintenanceValues(args)
	if err != nil {
		return nil, err
	}
	return stmt.Stmt.Exec(values)
}
func (stmt *maintenanceStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	release, err := stmt.conn.acquire(ctx)
	if err != nil {
		return nil, err
	}
	var rows driver.Rows
	if queryer, ok := stmt.Stmt.(driver.StmtQueryContext); ok {
		rows, err = queryer.QueryContext(ctx, args)
	} else {
		var values []driver.Value
		values, err = maintenanceValues(args)
		if err == nil {
			rows, err = stmt.Stmt.Query(values)
		}
	}
	if err != nil {
		release()
		return nil, err
	}
	return &maintenanceRows{Rows: rows, release: release}, nil
}
func (stmt *maintenanceStmt) CheckNamedValue(value *driver.NamedValue) error {
	if checker, ok := stmt.Stmt.(driver.NamedValueChecker); ok {
		return checker.CheckNamedValue(value)
	}
	return stmt.conn.CheckNamedValue(value)
}
func (stmt *maintenanceStmt) ColumnConverter(index int) driver.ValueConverter {
	if converter, ok := stmt.Stmt.(driver.ColumnConverter); ok {
		return converter.ColumnConverter(index)
	}
	return driver.DefaultParameterConverter
}
func maintenanceValues(args []driver.NamedValue) ([]driver.Value, error) {
	values := make([]driver.Value, len(args))
	for index, arg := range args {
		if arg.Name != "" {
			return nil, errors.New("数据库驱动不支持命名参数")
		}
		values[index] = arg.Value
	}
	return values, nil
}

type maintenanceRows struct {
	driver.Rows
	release func()
}

func (rows *maintenanceRows) Close() error { defer rows.release(); return rows.Rows.Close() }
func (rows *maintenanceRows) HasNextResultSet() bool {
	if next, ok := rows.Rows.(driver.RowsNextResultSet); ok {
		return next.HasNextResultSet()
	}
	return false
}
func (rows *maintenanceRows) NextResultSet() error {
	if next, ok := rows.Rows.(driver.RowsNextResultSet); ok {
		return next.NextResultSet()
	}
	return io.EOF
}
func (rows *maintenanceRows) ColumnTypeDatabaseTypeName(index int) string {
	if columns, ok := rows.Rows.(driver.RowsColumnTypeDatabaseTypeName); ok {
		return columns.ColumnTypeDatabaseTypeName(index)
	}
	return ""
}
func (rows *maintenanceRows) ColumnTypeLength(index int) (int64, bool) {
	if columns, ok := rows.Rows.(driver.RowsColumnTypeLength); ok {
		return columns.ColumnTypeLength(index)
	}
	return 0, false
}
func (rows *maintenanceRows) ColumnTypeNullable(index int) (bool, bool) {
	if columns, ok := rows.Rows.(driver.RowsColumnTypeNullable); ok {
		return columns.ColumnTypeNullable(index)
	}
	return false, false
}
func (rows *maintenanceRows) ColumnTypePrecisionScale(index int) (int64, int64, bool) {
	if columns, ok := rows.Rows.(driver.RowsColumnTypePrecisionScale); ok {
		return columns.ColumnTypePrecisionScale(index)
	}
	return 0, 0, false
}
func (rows *maintenanceRows) ColumnTypeScanType(index int) reflect.Type {
	if columns, ok := rows.Rows.(driver.RowsColumnTypeScanType); ok {
		return columns.ColumnTypeScanType(index)
	}
	return reflect.TypeFor[any]()
}
