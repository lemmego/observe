package observe_test

import (
	"context"
	"database/sql/driver"
	"errors"
	"io"
	"reflect"
	"sync"
)

// A fake driver implementing every optional interface, so a wrapper can be
// checked for forwarding rather than swallowing.
//
// Two variants matter: `full` implements everything, and `bare` implements
// only what driver.Conn and driver.Stmt require. A wrapper has to behave
// correctly over both — forwarding for the first, reproducing database/sql's
// fallback for the second — and those are different bugs.

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) record(name string) {
	l.mu.Lock()
	l.calls = append(l.calls, name)
	l.mu.Unlock()
}

func (l *callLog) saw(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, call := range l.calls {
		if call == name {
			return true
		}
	}
	return false
}

// --- the fully-featured connection ---

type fullConn struct{ log *callLog }

var (
	_ driver.Conn               = (*fullConn)(nil)
	_ driver.Queryer            = (*fullConn)(nil)
	_ driver.QueryerContext     = (*fullConn)(nil)
	_ driver.Execer             = (*fullConn)(nil)
	_ driver.ExecerContext      = (*fullConn)(nil)
	_ driver.ConnPrepareContext = (*fullConn)(nil)
	_ driver.ConnBeginTx        = (*fullConn)(nil)
	_ driver.SessionResetter    = (*fullConn)(nil)
	_ driver.Validator          = (*fullConn)(nil)
	_ driver.Pinger             = (*fullConn)(nil)
	_ driver.NamedValueChecker  = (*fullConn)(nil)
)

func (c *fullConn) Prepare(string) (driver.Stmt, error) {
	c.log.record("Prepare")
	return &fullStmt{log: c.log}, nil
}
func (c *fullConn) Close() error { c.log.record("Close"); return nil }
func (c *fullConn) Begin() (driver.Tx, error) {
	c.log.record("Begin")
	return &fakeTx{log: c.log}, nil
}
func (c *fullConn) Query(string, []driver.Value) (driver.Rows, error) {
	c.log.record("Query")
	return &fakeRows{}, nil
}
func (c *fullConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.log.record("QueryContext")
	return &fakeRows{}, nil
}
func (c *fullConn) Exec(string, []driver.Value) (driver.Result, error) {
	c.log.record("Exec")
	return fakeResult{}, nil
}
func (c *fullConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.log.record("ExecContext")
	return fakeResult{}, nil
}
func (c *fullConn) PrepareContext(context.Context, string) (driver.Stmt, error) {
	c.log.record("PrepareContext")
	return &fullStmt{log: c.log}, nil
}
func (c *fullConn) BeginTx(context.Context, driver.TxOptions) (driver.Tx, error) {
	c.log.record("BeginTx")
	return &fakeTx{log: c.log}, nil
}
func (c *fullConn) ResetSession(context.Context) error { c.log.record("ResetSession"); return nil }
func (c *fullConn) IsValid() bool                      { c.log.record("IsValid"); return true }
func (c *fullConn) Ping(context.Context) error         { c.log.record("Ping"); return nil }
func (c *fullConn) CheckNamedValue(nv *driver.NamedValue) error {
	c.log.record("CheckNamedValue")
	// Stand in for lib/pq's array handling: a checker that accepts a type the
	// default converter would refuse. If the wrapper fails to forward this,
	// the value never binds.
	if _, ok := nv.Value.([]string); ok {
		nv.Value = "{converted}"
	}
	return nil
}

type fullStmt struct{ log *callLog }

var (
	_ driver.Stmt              = (*fullStmt)(nil)
	_ driver.StmtQueryContext  = (*fullStmt)(nil)
	_ driver.StmtExecContext   = (*fullStmt)(nil)
	_ driver.NamedValueChecker = (*fullStmt)(nil)
)

func (s *fullStmt) Close() error  { s.log.record("Stmt.Close"); return nil }
func (s *fullStmt) NumInput() int { return -1 }
func (s *fullStmt) Exec([]driver.Value) (driver.Result, error) {
	s.log.record("Stmt.Exec")
	return fakeResult{}, nil
}
func (s *fullStmt) Query([]driver.Value) (driver.Rows, error) {
	s.log.record("Stmt.Query")
	return &fakeRows{}, nil
}
func (s *fullStmt) ExecContext(context.Context, []driver.NamedValue) (driver.Result, error) {
	s.log.record("Stmt.ExecContext")
	return fakeResult{}, nil
}
func (s *fullStmt) QueryContext(context.Context, []driver.NamedValue) (driver.Rows, error) {
	s.log.record("Stmt.QueryContext")
	return &fakeRows{}, nil
}
func (s *fullStmt) CheckNamedValue(nv *driver.NamedValue) error {
	s.log.record("Stmt.CheckNamedValue")
	return nil
}

// --- the minimal connection: only what the required interfaces demand ---

type bareConn struct{ log *callLog }

var _ driver.Conn = (*bareConn)(nil)

func (c *bareConn) Prepare(string) (driver.Stmt, error) {
	c.log.record("Prepare")
	return &bareStmt{log: c.log}, nil
}
func (c *bareConn) Close() error { return nil }
func (c *bareConn) Begin() (driver.Tx, error) {
	c.log.record("Begin")
	return &fakeTx{log: c.log}, nil
}

type bareStmt struct{ log *callLog }

var _ driver.Stmt = (*bareStmt)(nil)

func (s *bareStmt) Close() error  { return nil }
func (s *bareStmt) NumInput() int { return -1 }
func (s *bareStmt) Exec([]driver.Value) (driver.Result, error) {
	s.log.record("Stmt.Exec")
	return fakeResult{}, nil
}
func (s *bareStmt) Query([]driver.Value) (driver.Rows, error) {
	s.log.record("Stmt.Query")
	return &fakeRows{}, nil
}

// --- shared bits ---

type fakeTx struct{ log *callLog }

func (t *fakeTx) Commit() error   { t.log.record("Commit"); return nil }
func (t *fakeTx) Rollback() error { t.log.record("Rollback"); return nil }

type fakeResult struct{}

func (fakeResult) LastInsertId() (int64, error) { return 1, nil }
func (fakeResult) RowsAffected() (int64, error) { return 3, nil }

// fakeRows implements every optional Rows interface, so a test can assert the
// wrapper passes them through rather than hiding them.
type fakeRows struct{ closed bool }

var (
	_ driver.Rows                           = (*fakeRows)(nil)
	_ driver.RowsNextResultSet              = (*fakeRows)(nil)
	_ driver.RowsColumnTypeScanType         = (*fakeRows)(nil)
	_ driver.RowsColumnTypeDatabaseTypeName = (*fakeRows)(nil)
	_ driver.RowsColumnTypeLength           = (*fakeRows)(nil)
	_ driver.RowsColumnTypeNullable         = (*fakeRows)(nil)
	_ driver.RowsColumnTypePrecisionScale   = (*fakeRows)(nil)
)

func (r *fakeRows) Columns() []string { return []string{"id"} }
func (r *fakeRows) Close() error      { r.closed = true; return nil }
func (r *fakeRows) Next(dest []driver.Value) error {
	if r.closed {
		return io.EOF
	}
	r.closed = true
	dest[0] = int64(1)
	return nil
}
func (r *fakeRows) HasNextResultSet() bool { return false }
func (r *fakeRows) NextResultSet() error   { return io.EOF }
func (r *fakeRows) ColumnTypeScanType(int) reflect.Type {
	return reflect.TypeOf(int64(0))
}
func (r *fakeRows) ColumnTypeDatabaseTypeName(int) string { return "BIGINT" }
func (r *fakeRows) ColumnTypeLength(int) (int64, bool)    { return 64, true }
func (r *fakeRows) ColumnTypeNullable(int) (bool, bool)   { return false, true }
func (r *fakeRows) ColumnTypePrecisionScale(int) (int64, int64, bool) {
	return 10, 2, true
}

// --- drivers ---

type fullDriver struct{ log *callLog }

var (
	_ driver.Driver        = (*fullDriver)(nil)
	_ driver.DriverContext = (*fullDriver)(nil)
)

func (d *fullDriver) Open(string) (driver.Conn, error) { return &fullConn{log: d.log}, nil }
func (d *fullDriver) OpenConnector(string) (driver.Connector, error) {
	d.log.record("OpenConnector")
	return &fullConnector{log: d.log, driver: d}, nil
}

type fullConnector struct {
	log    *callLog
	driver driver.Driver
}

func (c *fullConnector) Connect(context.Context) (driver.Conn, error) {
	c.log.record("Connect")
	return &fullConn{log: c.log}, nil
}
func (c *fullConnector) Driver() driver.Driver { return c.driver }

// bareDriver implements driver.Driver and nothing else.
type bareDriver struct{ log *callLog }

var _ driver.Driver = (*bareDriver)(nil)

func (d *bareDriver) Open(string) (driver.Conn, error) { return &bareConn{log: d.log}, nil }

var errFake = errors.New("fake failure")
