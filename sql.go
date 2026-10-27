package observe

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"log/slog"
	"time"
)

// WatcherSQLDriver is the watcher name entries from [Wrap] carry.
const WatcherSQLDriver = "sqldriver"

// Wrap instruments a database/sql driver, so every statement executed through
// it is recorded.
//
// This is the layer query recording belongs at, because it is below everything
// an application might choose. One wrapper covers lemmego/orm, GORM, Bun,
// sqlx, raw database/sql, lemmego/migration, tasker's SQL driver and the
// session store, and it does so without any of them knowing about it.
//
// Register the wrapped driver and point configuration at it:
//
//	func init() {
//	    sql.Register("postgres-observed", observe.Wrap(&pq.Driver{}))
//	}
//	// DB_DRIVER=postgres-observed
//
// It needs no framework change because the three places that matter —
// ormconnector, migration and tasker — all take the driver name from
// configuration. Two paths are not covered: a GORM dialector builds its own
// pool, so hand it an instrumented *sql.DB instead; and sql.OpenDB(connector)
// bypasses the driver-name registry, so wrap the driver.Connector with
// [WrapConnector].
//
// Entries are recorded to the default recorder, because sql.Register runs at
// init and there is nothing to hand a recorder to yet. Statements executed
// before [SetDefault] are not recorded.
func Wrap(d driver.Driver) driver.Driver {
	if d == nil {
		return nil
	}
	w := &observedDriver{wrapped: d}
	// database/sql prefers OpenConnector when the driver offers it, and most
	// modern drivers do. Reporting DriverContext only when the wrapped driver
	// has it keeps the feature detection honest in both directions.
	if _, ok := d.(driver.DriverContext); ok {
		return &observedDriverContext{observedDriver: w}
	}
	return w
}

// WrapConnector instruments a driver.Connector, for a pool opened with
// sql.OpenDB rather than sql.Open.
func WrapConnector(c driver.Connector) driver.Connector {
	if c == nil {
		return nil
	}
	return &observedConnector{wrapped: c}
}

// recordStatement is the one place a statement becomes an entry.
func recordStatement(ctx context.Context, query string, args []driver.NamedValue, started time.Time, err error, result driver.Result) {
	rec := Default()
	if rec == nil || !Recording(ctx) {
		return
	}

	// driver.ErrSkip is not a failure. It is how a driver says "I do not
	// implement this fast path, use the general one", after which database/sql
	// retries through Prepare — and the retry is what gets recorded. Treating
	// it as an error would report a failed query for every statement a driver
	// declines to special-case.
	if errors.Is(err, driver.ErrSkip) {
		return
	}

	rows := int64(-1)
	if result != nil {
		if n, rerr := result.RowsAffected(); rerr == nil {
			rows = n
		}
	}

	payload := sqlPayload{
		SQL:         query,
		Args:        renderArgs(args),
		Rows:        rows,
		Correlation: correlationOf(ctx),
	}
	data, merr := json.Marshal(payload)
	if merr != nil {
		// A payload that will not marshal is a bug here, not in the
		// application, and losing the entry is better than losing the query.
		slog.Error("observe: a statement's payload could not be encoded", "error", merr)
		return
	}

	entry := Entry{
		Kind:     KindQuery,
		At:       started,
		Duration: time.Since(started),
		Level:    slog.LevelInfo,
		Summary:  collapseWhitespace(query),
		Data:     data,
		Watcher:  WatcherSQLDriver,
	}
	if err != nil {
		entry.Level = slog.LevelError
		entry.Err = err.Error()
	}

	rec.Record(ctx, entry)
}

// sqlPayload is what a query entry carries.
type sqlPayload struct {
	SQL  string   `json:"sql"`
	Args []string `json:"args,omitempty"`
	Rows int64    `json:"rows"`

	// Correlation says why this statement is, or is not, tied to a request.
	//
	// Three answers rather than a bool, because an empty RequestID has two
	// quite different causes and a reader needs to tell them apart: work with
	// no request behind it is ordinary, while a driver that offers only the
	// legacy non-context interfaces means nothing on that path can ever be
	// correlated and no amount of middleware will fix it.
	Correlation Correlation `json:"correlation"`
}

// Correlation is why a statement is or is not tied to a request.
type Correlation string

const (
	// CorrelatedToRequest: the statement carries a request id.
	CorrelatedToRequest Correlation = "request"

	// NotUnderARequest: there was a context, and it carried no request id —
	// a worker, a scheduled task, a migration.
	NotUnderARequest Correlation = "none"

	// CorrelationUnavailable: the code path has no context at all, because
	// the driver implements only driver.Queryer or driver.Execer. Nothing can
	// correlate these, so a reader should stop looking for a cause.
	CorrelationUnavailable Correlation = "unavailable"
)

func correlationOf(ctx context.Context) Correlation {
	if ctx == nil {
		return CorrelationUnavailable
	}
	if _, unavailable := ctx.(contextless); unavailable {
		return CorrelationUnavailable
	}
	if RequestIDFrom(ctx) != "" {
		return CorrelatedToRequest
	}
	return NotUnderARequest
}

// contextless marks the legacy driver paths, which receive no context at all.
//
// A distinct type rather than context.Background(), because Background
// returns one shared value — so comparing against it would report every
// genuinely uncorrelated request as a path that cannot be correlated, which
// is the opposite of what this distinction is for.
type contextless struct{ context.Context }

var noContextAvailable = contextless{context.Background()}

// renderArgs turns bound parameters into strings.
//
// Rendered rather than stored as values because an argument may be any type a
// driver accepts, including one that will not marshal, and because the only
// thing anybody does with a bound argument in a debugger is read it. Every
// value goes through Redact first: a bound parameter is where a password
// arrives.
func renderArgs(args []driver.NamedValue) []string {
	if len(args) == 0 {
		return nil
	}
	out := make([]string, 0, len(args))
	for _, arg := range args {
		out = append(out, RedactValue(arg.Name, arg.Value))
	}
	return out
}

// ---------------------------------------------------------------------------
// driver.Driver
// ---------------------------------------------------------------------------

type observedDriver struct{ wrapped driver.Driver }

var _ driver.Driver = (*observedDriver)(nil)

func (d *observedDriver) Open(name string) (driver.Conn, error) {
	conn, err := d.wrapped.Open(name)
	if err != nil {
		return nil, err
	}
	return wrapConn(conn), nil
}

// observedDriverContext adds OpenConnector, which database/sql prefers.
//
// A separate type rather than a method on observedDriver, because
// database/sql decides by type assertion: a single type carrying
// OpenConnector would claim the interface for a wrapped driver that does not
// implement it, and the assertion would succeed where it should fail.
type observedDriverContext struct{ *observedDriver }

var (
	_ driver.Driver        = (*observedDriverContext)(nil)
	_ driver.DriverContext = (*observedDriverContext)(nil)
)

func (d *observedDriverContext) OpenConnector(name string) (driver.Connector, error) {
	connector, err := d.wrapped.(driver.DriverContext).OpenConnector(name)
	if err != nil {
		return nil, err
	}
	return &observedConnector{wrapped: connector}, nil
}

// ---------------------------------------------------------------------------
// driver.Connector
// ---------------------------------------------------------------------------

type observedConnector struct{ wrapped driver.Connector }

var _ driver.Connector = (*observedConnector)(nil)

func (c *observedConnector) Connect(ctx context.Context) (driver.Conn, error) {
	conn, err := c.wrapped.Connect(ctx)
	if err != nil {
		return nil, err
	}
	return wrapConn(conn), nil
}

func (c *observedConnector) Driver() driver.Driver { return Wrap(c.wrapped.Driver()) }
