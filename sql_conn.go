package observe

import (
	"context"
	"database/sql/driver"
	"errors"
	"time"
)

// The optional-interface problem, and how this file answers it.
//
// database/sql feature-detects a driver by type assertion across ten optional
// interfaces on a connection alone. A wrapper that embeds the connection and
// forgets one silently loses it: without NamedValueChecker, lib/pq's array
// binding stops working; without ConnBeginTx, an isolation level is ignored
// rather than refused. Nothing reports any of this — it is the same shape as
// the http.Hijacker bug this framework has already paid for twice, with ten
// methods instead of three.
//
// Declaring a type per combination would be 2^10 of them. So this wrapper
// claims every optional interface and, where the wrapped connection does not
// implement one, reproduces database/sql's own fallback exactly:
//
//	QueryerContext, Queryer       driver.ErrSkip, so sql retries via Prepare
//	ExecerContext, Execer         driver.ErrSkip
//	ConnPrepareContext            Prepare, dropping only the context
//	ConnBeginTx                   refuse the options sql would refuse, then Begin
//	SessionResetter               nil, which is what skipping it means
//	Validator                     true, which is what sql assumes
//	Pinger                        nil, which is what sql assumes
//	NamedValueChecker             driver.ErrSkip, so the default converter runs
//
// Each has a compile-time assertion below and a test asserting it forwards
// rather than swallows. The assertion catches a wrong signature; only the
// test catches an omission.
//
// driver.Rows is deliberately NOT wrapped. Its six optional interfaces decide
// what sql.ColumnType reports, and the only thing wrapping it would buy is a
// row count for queries — which lemmego/orm's own logger also declines to
// provide, since rows are counted as they are scanned long after the
// statement returns. Passing the rows through untouched means every one of
// those interfaces keeps working natively, at no risk.

type observedConn struct{ wrapped driver.Conn }

var (
	_ driver.Conn               = (*observedConn)(nil)
	_ driver.Queryer            = (*observedConn)(nil)
	_ driver.QueryerContext     = (*observedConn)(nil)
	_ driver.Execer             = (*observedConn)(nil)
	_ driver.ExecerContext      = (*observedConn)(nil)
	_ driver.ConnPrepareContext = (*observedConn)(nil)
	_ driver.ConnBeginTx        = (*observedConn)(nil)
	_ driver.SessionResetter    = (*observedConn)(nil)
	_ driver.Validator          = (*observedConn)(nil)
	_ driver.Pinger             = (*observedConn)(nil)
	_ driver.NamedValueChecker  = (*observedConn)(nil)
)

func wrapConn(c driver.Conn) driver.Conn {
	if c == nil {
		return nil
	}
	return &observedConn{wrapped: c}
}

// Unwrap returns the wrapped connection, for a caller that needs the real one.
func (c *observedConn) Unwrap() driver.Conn { return c.wrapped }

func (c *observedConn) Prepare(query string) (driver.Stmt, error) {
	stmt, err := c.wrapped.Prepare(query)
	if err != nil {
		return nil, err
	}
	return &observedStmt{wrapped: stmt, query: query}, nil
}

func (c *observedConn) Close() error { return c.wrapped.Close() }

func (c *observedConn) Begin() (driver.Tx, error) {
	//nolint:staticcheck // Begin is required by driver.Conn.
	tx, err := c.wrapped.Begin()
	if err != nil {
		return nil, err
	}
	return &observedTx{wrapped: tx, ctx: noContextAvailable, started: time.Now()}, nil
}

func (c *observedConn) PrepareContext(ctx context.Context, query string) (driver.Stmt, error) {
	var stmt driver.Stmt
	var err error
	if pc, ok := c.wrapped.(driver.ConnPrepareContext); ok {
		stmt, err = pc.PrepareContext(ctx, query)
	} else {
		stmt, err = c.wrapped.Prepare(query)
	}
	if err != nil {
		return nil, err
	}
	return &observedStmt{wrapped: stmt, query: query}, nil
}

func (c *observedConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	started := time.Now()

	if bt, ok := c.wrapped.(driver.ConnBeginTx); ok {
		tx, err := bt.BeginTx(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &observedTx{wrapped: tx, ctx: ctx, started: started}, nil
	}

	// Refuse exactly what database/sql refuses for a driver without
	// ConnBeginTx. Ignoring the options instead would hand back a transaction
	// at the wrong isolation level and say nothing, which is worse than the
	// error the caller would otherwise have seen.
	if driver.IsolationLevel(opts.Isolation) != driver.IsolationLevel(0) {
		return nil, errors.New("observe: the wrapped driver does not support custom isolation levels")
	}
	if opts.ReadOnly {
		return nil, errors.New("observe: the wrapped driver does not support read-only transactions")
	}

	//nolint:staticcheck // the fallback database/sql would itself take.
	tx, err := c.wrapped.Begin()
	if err != nil {
		return nil, err
	}
	return &observedTx{wrapped: tx, ctx: ctx, started: started}, nil
}

func (c *observedConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	q, ok := c.wrapped.(driver.Queryer)
	if !ok {
		return nil, driver.ErrSkip
	}
	started := time.Now()
	//nolint:staticcheck // the legacy path, used only when the driver has no context variant.
	rows, err := q.Query(query, args)
	// Uncorrelated by construction: there is no context on this path, so
	// there is nowhere a request id could have come from. Recorded anyway,
	// with Correlated false, because a query nobody can group is still a
	// query somebody needs to see.
	recordStatement(noContextAvailable, query, valuesToNamed(args), started, err, nil)
	return rows, err
}

func (c *observedConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	q, ok := c.wrapped.(driver.QueryerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	started := time.Now()
	rows, err := q.QueryContext(ctx, query, args)
	recordStatement(ctx, query, args, started, err, nil)
	return rows, err
}

func (c *observedConn) Exec(query string, args []driver.Value) (driver.Result, error) {
	e, ok := c.wrapped.(driver.Execer)
	if !ok {
		return nil, driver.ErrSkip
	}
	started := time.Now()
	//nolint:staticcheck // the legacy path.
	result, err := e.Exec(query, args)
	recordStatement(noContextAvailable, query, valuesToNamed(args), started, err, result)
	return result, err
}

func (c *observedConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	e, ok := c.wrapped.(driver.ExecerContext)
	if !ok {
		return nil, driver.ErrSkip
	}
	started := time.Now()
	result, err := e.ExecContext(ctx, query, args)
	recordStatement(ctx, query, args, started, err, result)
	return result, err
}

func (c *observedConn) ResetSession(ctx context.Context) error {
	if r, ok := c.wrapped.(driver.SessionResetter); ok {
		return r.ResetSession(ctx)
	}
	return nil
}

func (c *observedConn) IsValid() bool {
	if v, ok := c.wrapped.(driver.Validator); ok {
		return v.IsValid()
	}
	return true
}

func (c *observedConn) Ping(ctx context.Context) error {
	if p, ok := c.wrapped.(driver.Pinger); ok {
		return p.Ping(ctx)
	}
	return nil
}

func (c *observedConn) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := c.wrapped.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	// ErrSkip is how a checker says "use the default converter", which is
	// what database/sql does when there is no checker at all.
	return driver.ErrSkip
}

// ---------------------------------------------------------------------------
// driver.Stmt
// ---------------------------------------------------------------------------

type observedStmt struct {
	wrapped driver.Stmt
	query   string
}

var (
	_ driver.Stmt              = (*observedStmt)(nil)
	_ driver.StmtQueryContext  = (*observedStmt)(nil)
	_ driver.StmtExecContext   = (*observedStmt)(nil)
	_ driver.NamedValueChecker = (*observedStmt)(nil)
)

func (s *observedStmt) Close() error  { return s.wrapped.Close() }
func (s *observedStmt) NumInput() int { return s.wrapped.NumInput() }

func (s *observedStmt) Exec(args []driver.Value) (driver.Result, error) {
	started := time.Now()
	//nolint:staticcheck // required by driver.Stmt.
	result, err := s.wrapped.Exec(args)
	recordStatement(noContextAvailable, s.query, valuesToNamed(args), started, err, result)
	return result, err
}

func (s *observedStmt) Query(args []driver.Value) (driver.Rows, error) {
	started := time.Now()
	//nolint:staticcheck // required by driver.Stmt.
	rows, err := s.wrapped.Query(args)
	recordStatement(noContextAvailable, s.query, valuesToNamed(args), started, err, nil)
	return rows, err
}

func (s *observedStmt) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	ec, ok := s.wrapped.(driver.StmtExecContext)
	if !ok {
		// Reproduce database/sql's fallback: drop the named values to
		// positional ones and take the legacy path. It refuses named
		// parameters there, and so must this.
		values, err := namedToValues(args)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		//nolint:staticcheck // the fallback database/sql would itself take.
		result, err := s.wrapped.Exec(values)
		recordStatement(ctx, s.query, args, started, err, result)
		return result, err
	}
	started := time.Now()
	result, err := ec.ExecContext(ctx, args)
	recordStatement(ctx, s.query, args, started, err, result)
	return result, err
}

func (s *observedStmt) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	qc, ok := s.wrapped.(driver.StmtQueryContext)
	if !ok {
		values, err := namedToValues(args)
		if err != nil {
			return nil, err
		}
		started := time.Now()
		//nolint:staticcheck // the fallback database/sql would itself take.
		rows, err := s.wrapped.Query(values)
		recordStatement(ctx, s.query, args, started, err, nil)
		return rows, err
	}
	started := time.Now()
	rows, err := qc.QueryContext(ctx, args)
	recordStatement(ctx, s.query, args, started, err, nil)
	return rows, err
}

func (s *observedStmt) CheckNamedValue(nv *driver.NamedValue) error {
	if ch, ok := s.wrapped.(driver.NamedValueChecker); ok {
		return ch.CheckNamedValue(nv)
	}
	return driver.ErrSkip
}

// ---------------------------------------------------------------------------
// driver.Tx
// ---------------------------------------------------------------------------

// observedTx records how a transaction ended.
//
// Wrapped because driver.Tx has exactly two methods and no optional
// interfaces, so there is nothing to lose — and because "this request opened
// a transaction and rolled it back" is a question worth being able to answer.
type observedTx struct {
	wrapped driver.Tx
	ctx     context.Context
	started time.Time
}

var _ driver.Tx = (*observedTx)(nil)

func (t *observedTx) Commit() error {
	err := t.wrapped.Commit()
	t.record("COMMIT", err)
	return err
}

func (t *observedTx) Rollback() error {
	err := t.wrapped.Rollback()
	t.record("ROLLBACK", err)
	return err
}

func (t *observedTx) record(what string, err error) {
	recordStatement(t.ctx, what, nil, t.started, err, nil)
}

// ---------------------------------------------------------------------------
// argument conversion
// ---------------------------------------------------------------------------

func valuesToNamed(args []driver.Value) []driver.NamedValue {
	if len(args) == 0 {
		return nil
	}
	out := make([]driver.NamedValue, len(args))
	for i, v := range args {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

// namedToValues is database/sql's own conversion for the legacy path, which
// refuses named parameters because a positional interface cannot express one.
func namedToValues(args []driver.NamedValue) ([]driver.Value, error) {
	out := make([]driver.Value, len(args))
	for i, nv := range args {
		if nv.Name != "" {
			return nil, errors.New("observe: the wrapped driver does not support named parameters")
		}
		out[i] = nv.Value
	}
	return out, nil
}
