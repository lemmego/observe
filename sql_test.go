package observe_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// TestTheWrapperForwardsEveryOptionalInterface is the test this package most
// needs.
//
// database/sql feature-detects a driver by type assertion, so a wrapper that
// claims an interface and forwards nothing silently disables a driver
// feature: without NamedValueChecker lib/pq's array binding stops working,
// without ConnBeginTx an isolation level is ignored rather than refused. The
// compile-time assertions in sql_conn.go catch a wrong signature; only this
// catches a method that was declared and left empty.
//
// Verified by deleting a forwarder and watching the matching subtest fail.
func TestTheWrapperForwardsEveryOptionalInterface(t *testing.T) {
	cases := []struct {
		name string
		call func(t *testing.T, conn driver.Conn)
		want string
	}{
		{"QueryerContext", func(t *testing.T, c driver.Conn) {
			q := assertImplements[driver.QueryerContext](t, c)
			if _, err := q.QueryContext(context.Background(), "SELECT 1", nil); err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
		}, "QueryContext"},

		{"Queryer", func(t *testing.T, c driver.Conn) {
			q := assertImplements[driver.Queryer](t, c)
			//nolint:staticcheck // exercising the legacy path on purpose.
			if _, err := q.Query("SELECT 1", nil); err != nil {
				t.Fatalf("Query: %v", err)
			}
		}, "Query"},

		{"ExecerContext", func(t *testing.T, c driver.Conn) {
			e := assertImplements[driver.ExecerContext](t, c)
			if _, err := e.ExecContext(context.Background(), "DELETE FROM t", nil); err != nil {
				t.Fatalf("ExecContext: %v", err)
			}
		}, "ExecContext"},

		{"Execer", func(t *testing.T, c driver.Conn) {
			e := assertImplements[driver.Execer](t, c)
			//nolint:staticcheck // exercising the legacy path on purpose.
			if _, err := e.Exec("DELETE FROM t", nil); err != nil {
				t.Fatalf("Exec: %v", err)
			}
		}, "Exec"},

		{"ConnPrepareContext", func(t *testing.T, c driver.Conn) {
			p := assertImplements[driver.ConnPrepareContext](t, c)
			if _, err := p.PrepareContext(context.Background(), "SELECT 1"); err != nil {
				t.Fatalf("PrepareContext: %v", err)
			}
		}, "PrepareContext"},

		{"ConnBeginTx", func(t *testing.T, c driver.Conn) {
			b := assertImplements[driver.ConnBeginTx](t, c)
			if _, err := b.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
				t.Fatalf("BeginTx: %v", err)
			}
		}, "BeginTx"},

		{"SessionResetter", func(t *testing.T, c driver.Conn) {
			r := assertImplements[driver.SessionResetter](t, c)
			if err := r.ResetSession(context.Background()); err != nil {
				t.Fatalf("ResetSession: %v", err)
			}
		}, "ResetSession"},

		{"Validator", func(t *testing.T, c driver.Conn) {
			v := assertImplements[driver.Validator](t, c)
			if !v.IsValid() {
				t.Fatal("IsValid returned false")
			}
		}, "IsValid"},

		{"Pinger", func(t *testing.T, c driver.Conn) {
			p := assertImplements[driver.Pinger](t, c)
			if err := p.Ping(context.Background()); err != nil {
				t.Fatalf("Ping: %v", err)
			}
		}, "Ping"},

		{"NamedValueChecker", func(t *testing.T, c driver.Conn) {
			ch := assertImplements[driver.NamedValueChecker](t, c)
			nv := driver.NamedValue{Ordinal: 1, Value: []string{"a", "b"}}
			if err := ch.CheckNamedValue(&nv); err != nil {
				t.Fatalf("CheckNamedValue: %v", err)
			}
			// The forwarding has to carry the conversion back out, not just
			// call through: this is the shape of pq's array binding.
			if nv.Value != "{converted}" {
				t.Errorf("the checker's conversion was lost: got %v", nv.Value)
			}
		}, "CheckNamedValue"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &callLog{}
			wrapped := observe.Wrap(&fullDriver{log: log})

			conn, err := wrapped.Open("dsn")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			tc.call(t, conn)

			if !log.saw(tc.want) {
				t.Errorf("the wrapper did not forward to the driver's %s; "+
					"it claims the interface and swallows the call, which "+
					"disables the feature silently", tc.want)
			}
		})
	}
}

// TestStatementForwardingReachesTheDriver covers the Stmt interfaces, which
// are a separate set from the connection's.
func TestStatementForwardingReachesTheDriver(t *testing.T) {
	cases := []struct {
		name string
		call func(t *testing.T, stmt driver.Stmt)
		want string
	}{
		{"StmtQueryContext", func(t *testing.T, s driver.Stmt) {
			q := assertImplements[driver.StmtQueryContext](t, s)
			if _, err := q.QueryContext(context.Background(), nil); err != nil {
				t.Fatalf("QueryContext: %v", err)
			}
		}, "Stmt.QueryContext"},

		{"StmtExecContext", func(t *testing.T, s driver.Stmt) {
			e := assertImplements[driver.StmtExecContext](t, s)
			if _, err := e.ExecContext(context.Background(), nil); err != nil {
				t.Fatalf("ExecContext: %v", err)
			}
		}, "Stmt.ExecContext"},

		{"Exec", func(t *testing.T, s driver.Stmt) {
			if _, err := s.Exec(nil); err != nil {
				t.Fatalf("Exec: %v", err)
			}
		}, "Stmt.Exec"},

		{"Query", func(t *testing.T, s driver.Stmt) {
			if _, err := s.Query(nil); err != nil {
				t.Fatalf("Query: %v", err)
			}
		}, "Stmt.Query"},

		{"NamedValueChecker", func(t *testing.T, s driver.Stmt) {
			ch := assertImplements[driver.NamedValueChecker](t, s)
			nv := driver.NamedValue{Ordinal: 1, Value: int64(1)}
			if err := ch.CheckNamedValue(&nv); err != nil {
				t.Fatalf("CheckNamedValue: %v", err)
			}
		}, "Stmt.CheckNamedValue"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := &callLog{}
			conn, err := observe.Wrap(&fullDriver{log: log}).Open("dsn")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			stmt, err := conn.Prepare("SELECT 1")
			if err != nil {
				t.Fatalf("Prepare: %v", err)
			}

			tc.call(t, stmt)

			if !log.saw(tc.want) {
				t.Errorf("the wrapper did not forward to the statement's %s", tc.want)
			}
		})
	}
}

// TestADriverWithoutAnOptionalInterfaceGetsTheRightFallback is the other half.
//
// The wrapper claims every interface, so a bare driver must get database/sql's
// own fallback rather than a silent success. ErrSkip is what sends sql back
// through Prepare; a nil error with no rows would look like a query that
// returned nothing.
func TestADriverWithoutAnOptionalInterfaceGetsTheRightFallback(t *testing.T) {
	log := &callLog{}
	conn, err := observe.Wrap(&bareDriver{log: log}).Open("dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	t.Run("QueryContext skips", func(t *testing.T) {
		q := assertImplements[driver.QueryerContext](t, conn)
		if _, err := q.QueryContext(context.Background(), "SELECT 1", nil); !errorsIsSkip(err) {
			t.Errorf("want driver.ErrSkip so database/sql retries via Prepare, got %v", err)
		}
	})

	t.Run("ExecContext skips", func(t *testing.T) {
		e := assertImplements[driver.ExecerContext](t, conn)
		if _, err := e.ExecContext(context.Background(), "DELETE FROM t", nil); !errorsIsSkip(err) {
			t.Errorf("want driver.ErrSkip, got %v", err)
		}
	})

	t.Run("CheckNamedValue skips to the default converter", func(t *testing.T) {
		ch := assertImplements[driver.NamedValueChecker](t, conn)
		nv := driver.NamedValue{Ordinal: 1, Value: int64(1)}
		if err := ch.CheckNamedValue(&nv); !errorsIsSkip(err) {
			t.Errorf("want driver.ErrSkip so the default converter runs, got %v", err)
		}
	})

	t.Run("PrepareContext falls back to Prepare", func(t *testing.T) {
		p := assertImplements[driver.ConnPrepareContext](t, conn)
		if _, err := p.PrepareContext(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("PrepareContext: %v", err)
		}
		if !log.saw("Prepare") {
			t.Error("PrepareContext did not fall back to the driver's Prepare")
		}
	})

	t.Run("BeginTx refuses what database/sql would refuse", func(t *testing.T) {
		b := assertImplements[driver.ConnBeginTx](t, conn)

		// A default transaction is fine and must reach Begin.
		if _, err := b.BeginTx(context.Background(), driver.TxOptions{}); err != nil {
			t.Fatalf("a default BeginTx: %v", err)
		}
		if !log.saw("Begin") {
			t.Error("BeginTx did not fall back to the driver's Begin")
		}

		// A custom isolation level must be an error, not silently ignored.
		// Ignoring it would hand back a transaction at the wrong isolation
		// level and say nothing.
		_, err := b.BeginTx(context.Background(), driver.TxOptions{
			Isolation: driver.IsolationLevel(sql.LevelSerializable),
		})
		if err == nil {
			t.Error("a custom isolation level was accepted by a driver that " +
				"cannot honour it, so the transaction would silently run at " +
				"the wrong level")
		}

		if _, err := b.BeginTx(context.Background(), driver.TxOptions{ReadOnly: true}); err == nil {
			t.Error("a read-only transaction was accepted by a driver that cannot honour it")
		}
	})

	t.Run("Pinger and Validator assume what database/sql assumes", func(t *testing.T) {
		if err := assertImplements[driver.Pinger](t, conn).Ping(context.Background()); err != nil {
			t.Errorf("Ping on a driver without Pinger should succeed, got %v", err)
		}
		if !assertImplements[driver.Validator](t, conn).IsValid() {
			t.Error("IsValid on a driver without Validator should be true")
		}
	})
}

// TestDriverContextIsClaimedOnlyWhenTheDriverHasIt guards the one place a
// type split was used instead of claim-and-degrade.
//
// A single type carrying OpenConnector would make the assertion succeed for a
// driver that cannot do it, which is the opposite of feature detection.
func TestDriverContextIsClaimedOnlyWhenTheDriverHasIt(t *testing.T) {
	log := &callLog{}

	if _, ok := observe.Wrap(&fullDriver{log: log}).(driver.DriverContext); !ok {
		t.Error("a driver implementing DriverContext lost it through the wrapper, " +
			"so database/sql will fall back to the slower dsn path")
	}
	if _, ok := observe.Wrap(&bareDriver{log: log}).(driver.DriverContext); ok {
		t.Error("a driver without DriverContext gained it through the wrapper, " +
			"so database/sql will call a method the driver cannot answer")
	}
}

// TestRowsAreNotWrapped states the deliberate omission as a test, so changing
// it is a decision rather than an accident.
//
// driver.Rows has six optional interfaces deciding what sql.ColumnType
// reports. Passing the rows through untouched keeps every one working
// natively; the only thing wrapping them would buy is a row count for
// queries, which lemmego/orm's own logger also declines to provide.
func TestRowsAreNotWrapped(t *testing.T) {
	log := &callLog{}
	conn, err := observe.Wrap(&fullDriver{log: log}).Open("dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	rows, err := assertImplements[driver.QueryerContext](t, conn).
		QueryContext(context.Background(), "SELECT 1", nil)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	if _, ok := rows.(*fakeRows); !ok {
		t.Fatalf("rows were wrapped (%T); every optional Rows interface is at risk", rows)
	}

	// And the interfaces a caller actually reaches for still answer.
	scan := assertImplements[driver.RowsColumnTypeScanType](t, rows)
	if got := scan.ColumnTypeScanType(0); got != reflect.TypeOf(int64(0)) {
		t.Errorf("ColumnTypeScanType = %v, want int64", got)
	}
	name := assertImplements[driver.RowsColumnTypeDatabaseTypeName](t, rows)
	if got := name.ColumnTypeDatabaseTypeName(0); got != "BIGINT" {
		t.Errorf("ColumnTypeDatabaseTypeName = %q, want BIGINT", got)
	}
}

// TestStatementsAreRecordedWithTheirRequest is the point of the whole
// wrapper.
func TestStatementsAreRecordedWithTheirRequest(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	conn, err := observe.Wrap(&fullDriver{log: &callLog{}}).Open("dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	ctx := observe.WithRequestID(context.Background(), "req-1")
	if _, err := assertImplements[driver.QueryerContext](t, conn).
		QueryContext(ctx, "SELECT * FROM users WHERE id = $1", []driver.NamedValue{
			{Ordinal: 1, Value: int64(42)},
		}); err != nil {
		t.Fatalf("QueryContext: %v", err)
	}

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries under the request, want 1", len(entries))
	}

	entry := entries[0]
	if entry.Kind != observe.KindQuery {
		t.Errorf("Kind = %q, want query", entry.Kind)
	}
	if entry.Watcher != observe.WatcherSQLDriver {
		t.Errorf("Watcher = %q, want %q", entry.Watcher, observe.WatcherSQLDriver)
	}
	if entry.RequestID != "req-1" {
		t.Errorf("RequestID = %q; the statement was not correlated to its request", entry.RequestID)
	}
	if entry.Duration <= 0 {
		t.Error("Duration was not measured")
	}
}

// TestALegacyPathIsRecordedUncorrelatedRatherThanDropped states the honest
// limitation.
//
// driver.Queryer has no context, so there is nowhere a request id could come
// from. The statement is still worth seeing, so it is recorded with
// correlated false rather than discarded.
func TestALegacyPathIsRecordedUncorrelatedRatherThanDropped(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	conn, err := observe.Wrap(&fullDriver{log: &callLog{}}).Open("dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	//nolint:staticcheck // the legacy path is the subject.
	if _, err := assertImplements[driver.Queryer](t, conn).Query("SELECT 1", nil); err != nil {
		t.Fatalf("Query: %v", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the statement recorded anyway", len(entries))
	}
	if entries[0].RequestID != "" {
		t.Errorf("RequestID = %q, want empty on a path with no context", entries[0].RequestID)
	}
}

// TestErrSkipIsNotRecordedAsAFailure guards a mistake that would make every
// dashboard full of red.
//
// driver.ErrSkip is how a driver declines a fast path, after which
// database/sql retries through Prepare — and the retry is what gets recorded.
// Treating the decline as an error would report a failed query for every
// statement a driver does not special-case.
func TestErrSkipIsNotRecordedAsAFailure(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	conn, err := observe.Wrap(&bareDriver{log: &callLog{}}).Open("dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	//nolint:errcheck // the ErrSkip is the subject.
	assertImplements[driver.QueryerContext](t, conn).
		QueryContext(context.Background(), "SELECT 1", nil)

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("an ErrSkip was recorded as a statement (%d entries); every "+
			"declined fast path would show as a failed query", len(entries))
	}
}

func assertImplements[T any](t *testing.T, value any) T {
	t.Helper()
	typed, ok := value.(T)
	if !ok {
		var zero T
		t.Fatalf("%T does not implement %T; database/sql detects this by type "+
			"assertion, so the feature is silently unavailable", value, zero)
	}
	return typed
}

func errorsIsSkip(err error) bool { return err == driver.ErrSkip }

// TestCorrelationTellsItsThreeCausesApart.
//
// An empty request id has two quite different causes and a reader has to be
// able to tell them apart: ordinary work with no request behind it, versus a
// driver whose code path carries no context at all, where no amount of
// middleware will ever help. A single bool reported both as the same thing.
func TestCorrelationTellsItsThreeCausesApart(t *testing.T) {
	cases := []struct {
		name string
		run  func(t *testing.T, conn driver.Conn)
		want string
	}{
		{"under a request", func(t *testing.T, c driver.Conn) {
			ctx := observe.WithRequestID(context.Background(), "req-1")
			//nolint:errcheck // the recording is the subject.
			assertImplements[driver.QueryerContext](t, c).QueryContext(ctx, "SELECT 1", nil)
		}, `"correlation":"request"`},

		{"a context with no request", func(t *testing.T, c driver.Conn) {
			//nolint:errcheck // the recording is the subject.
			assertImplements[driver.QueryerContext](t, c).
				QueryContext(context.Background(), "SELECT 1", nil)
		}, `"correlation":"none"`},

		{"a path with no context", func(t *testing.T, c driver.Conn) {
			//nolint:staticcheck,errcheck // the legacy path is the subject.
			assertImplements[driver.Queryer](t, c).Query("SELECT 1", nil)
		}, `"correlation":"unavailable"`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := memory.New()
			recorder := observe.New(store)
			observe.SetDefault(recorder)
			t.Cleanup(func() { observe.SetDefault(nil) })

			conn, err := observe.Wrap(&fullDriver{log: &callLog{}}).Open("dsn")
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			tc.run(t, conn)

			if err := recorder.Close(context.Background()); err != nil {
				t.Fatalf("Close: %v", err)
			}
			entries, err := store.Entries(context.Background(), observe.Query{})
			if err != nil {
				t.Fatalf("Entries: %v", err)
			}
			if len(entries) != 1 {
				t.Fatalf("got %d entries, want 1", len(entries))
			}
			if got := string(entries[0].Data); !contains(got, tc.want) {
				t.Errorf("payload %s does not carry %s", got, tc.want)
			}
		})
	}
}
