// Package sqlstore keeps entries in the application's own database.
//
// This is the store an application should use, and the reason is process
// layout rather than durability. `lemmego run mcp:serve` boots a second
// process, and a worker is a third — so entries recorded by the web process
// are not in the memory of the process an agent is asking. A store every
// process can reach is the only arrangement where the question gets answered.
//
// It takes a *sql.DB and a dialect rather than the framework's connection
// type, so it is usable from a program that is not a Lemmego application.
// observe/provider does the resolving and passes both.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/lemmego/observe"
)

// DefaultTable is where entries live. Override it with [WithTable].
const DefaultTable = "observe_entries"

// Dialect names a SQL flavour. The spellings match lemmego/api/db's, so a
// caller can pass one straight through.
type Dialect string

const (
	Postgres Dialect = "postgres"
	MySQL    Dialect = "mysql"
	SQLite   Dialect = "sqlite"
)

// Store persists entries in a SQL database.
type Store struct {
	db      *sql.DB
	dialect Dialect
	table   string
}

var _ observe.Store = (*Store)(nil)

// Option configures a Store.
type Option func(*Store)

// WithTable changes the table entries are written to.
func WithTable(name string) Option {
	return func(s *Store) {
		if name != "" {
			s.table = name
		}
	}
}

// New returns a store over an open pool.
func New(pool *sql.DB, dialect Dialect, opts ...Option) (*Store, error) {
	if pool == nil {
		return nil, errors.New("observe/sqlstore: no database connection")
	}
	switch dialect {
	case Postgres, MySQL, SQLite:
	default:
		// Refused rather than guessed. Guessing would generate SQL the
		// database rejects at the first entry rather than at boot, and the
		// first entry is recorded from inside somebody's request.
		return nil, fmt.Errorf("observe/sqlstore: unsupported dialect %q", dialect)
	}

	store := &Store{db: pool, dialect: dialect, table: DefaultTable}
	for _, opt := range opts {
		opt(store)
	}
	return store, nil
}

// placeholder renders the nth bound parameter for this dialect.
func (s *Store) placeholder(n int) string {
	if s.dialect == Postgres {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// Record writes a batch.
//
// One statement for the whole batch rather than one per entry, because a
// request issuing forty queries should cost one round trip. The context is
// marked so the driver wrapper does not record this write — without that,
// storing an entry produces an entry, and nothing downstream stops the loop.
func (s *Store) Record(ctx context.Context, entries []observe.Entry) error {
	if len(entries) == 0 {
		return nil
	}
	ctx = observe.WithoutRecording(ctx)

	var columns strings.Builder
	columns.WriteString("INSERT INTO ")
	columns.WriteString(s.table)
	columns.WriteString(" (id, kind, request_id, recorded_at, duration_ns, level, summary, data, error, watcher) VALUES ")

	args := make([]any, 0, len(entries)*10)
	for i, entry := range entries {
		if entry.ID == "" {
			entry.ID = observe.NewID()
		}
		if entry.At.IsZero() {
			entry.At = time.Now()
		}
		if i > 0 {
			columns.WriteString(", ")
		}
		columns.WriteByte('(')
		for column := range 10 {
			if column > 0 {
				columns.WriteString(", ")
			}
			columns.WriteString(s.placeholder(len(args) + column + 1))
		}
		columns.WriteByte(')')

		// Stored in UTC. A naive column carries no offset, so writing local
		// time and reading it back shifts every timestamp by the machine's
		// zone — which would put a request's queries before the request.
		args = append(args,
			entry.ID,
			string(entry.Kind),
			entry.RequestID,
			entry.At.UTC(),
			int64(entry.Duration),
			int(entry.Level),
			entry.Summary,
			string(entry.Data),
			entry.Err,
			entry.Watcher,
		)
	}

	if _, err := s.db.ExecContext(ctx, columns.String(), args...); err != nil {
		return fmt.Errorf("observe/sqlstore: recording %d entries: %w", len(entries), err)
	}
	return nil
}

// Entries returns matching entries, newest first.
func (s *Store) Entries(ctx context.Context, q observe.Query) ([]observe.Entry, error) {
	ctx = observe.WithoutRecording(ctx)

	var where []string
	var args []any

	add := func(clause string, value any) {
		where = append(where, fmt.Sprintf(clause, s.placeholder(len(args)+1)))
		args = append(args, value)
	}

	if len(q.Kinds) > 0 {
		placeholders := make([]string, 0, len(q.Kinds))
		for _, kind := range q.Kinds {
			placeholders = append(placeholders, s.placeholder(len(args)+1))
			args = append(args, string(kind))
		}
		where = append(where, "kind IN ("+strings.Join(placeholders, ", ")+")")
	}
	if q.MinLevel != nil {
		add("level >= %s", int(*q.MinLevel))
	}
	if q.RequestID != "" {
		add("request_id = %s", q.RequestID)
	}
	if !q.Since.IsZero() {
		add("recorded_at >= %s", q.Since.UTC())
	}
	if !q.Until.IsZero() {
		add("recorded_at <= %s", q.Until.UTC())
	}
	if q.Contains != "" {
		// Matched against the summary and the payload, lower-cased on both
		// sides so the behaviour does not depend on the database's collation.
		pattern := "%" + strings.ToLower(q.Contains) + "%"
		clause := fmt.Sprintf("(LOWER(summary) LIKE %s OR LOWER(data) LIKE %s OR LOWER(error) LIKE %s)",
			s.placeholder(len(args)+1), s.placeholder(len(args)+2), s.placeholder(len(args)+3))
		where = append(where, clause)
		args = append(args, pattern, pattern, pattern)
	}

	// The cursor is exclusive and resolved against the ordering columns, not
	// against an offset: entries arrive continuously, so an offset shifts
	// under the reader and repeats rows at every page boundary.
	if q.Cursor.BeforeID != "" {
		anchor, err := s.anchor(ctx, q.Cursor.BeforeID)
		if err != nil {
			return nil, err
		}
		clause := fmt.Sprintf("(recorded_at < %s OR (recorded_at = %s AND id < %s))",
			s.placeholder(len(args)+1), s.placeholder(len(args)+2), s.placeholder(len(args)+3))
		where = append(where, clause)
		args = append(args, anchor.At.UTC(), anchor.At.UTC(), anchor.ID)
	}

	query := "SELECT id, kind, request_id, recorded_at, duration_ns, level, summary, data, error, watcher FROM " + s.table
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	// id as the tiebreak, so two entries recorded in the same instant have a
	// stable order and the cursor cannot loop on them.
	query += " ORDER BY recorded_at DESC, id DESC LIMIT " + s.placeholder(len(args)+1)
	args = append(args, observe.ClampLimit(q.Cursor.Limit))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("observe/sqlstore: reading entries: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil {
			slog.Debug("observe/sqlstore: closing rows", "error", cerr)
		}
	}()

	var out []observe.Entry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("observe/sqlstore: reading entries: %w", err)
	}
	return out, nil
}

// anchor reads the ordering columns of the cursor's entry.
func (s *Store) anchor(ctx context.Context, id string) (observe.Entry, error) {
	entry, err := s.Entry(ctx, id)
	if err != nil {
		// A cursor naming an entry that has since been pruned is ordinary
		// rather than an error — the reader was paging through a window that
		// moved. Anchoring at the beginning of time returns nothing, which is
		// the honest answer for "everything older than something that is gone".
		if errors.Is(err, observe.ErrNoEntry) {
			return observe.Entry{ID: "", At: time.Unix(0, 0)}, nil
		}
		return observe.Entry{}, err
	}
	return entry, nil
}

// Entry returns one entry.
func (s *Store) Entry(ctx context.Context, id string) (observe.Entry, error) {
	ctx = observe.WithoutRecording(ctx)

	query := "SELECT id, kind, request_id, recorded_at, duration_ns, level, summary, data, error, watcher FROM " +
		s.table + " WHERE id = " + s.placeholder(1)

	entry, err := scanEntry(s.db.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return observe.Entry{}, observe.ErrNoEntry
	}
	if err != nil {
		return observe.Entry{}, fmt.Errorf("observe/sqlstore: reading entry %s: %w", id, err)
	}
	return entry, nil
}

// Prune deletes entries older than a cutoff.
func (s *Store) Prune(ctx context.Context, before time.Time) (int64, error) {
	ctx = observe.WithoutRecording(ctx)

	query := "DELETE FROM " + s.table + " WHERE recorded_at < " + s.placeholder(1)
	result, err := s.db.ExecContext(ctx, query, before.UTC())
	if err != nil {
		return 0, fmt.Errorf("observe/sqlstore: pruning: %w", err)
	}
	removed, err := result.RowsAffected()
	if err != nil {
		// Reported rather than swallowed: a driver that cannot count is not a
		// failure to delete, and a scheduled prune logging "0 removed" forever
		// would look like a prune that is not running.
		slog.Debug("observe/sqlstore: the driver does not report affected rows", "error", err)
		return 0, nil
	}
	return removed, nil
}

// scanner is what both QueryRow and Rows satisfy.
type scanner interface {
	Scan(dest ...any) error
}

func scanEntry(row scanner) (observe.Entry, error) {
	var (
		entry     observe.Entry
		kind      string
		requestID sql.NullString
		recorded  time.Time
		duration  int64
		level     int
		summary   sql.NullString
		data      sql.NullString
		errText   sql.NullString
		watcher   sql.NullString
	)

	if err := row.Scan(&entry.ID, &kind, &requestID, &recorded, &duration,
		&level, &summary, &data, &errText, &watcher); err != nil {
		return observe.Entry{}, err
	}

	entry.Kind = observe.Kind(kind)
	entry.RequestID = requestID.String
	entry.At = recorded
	entry.Duration = time.Duration(duration)
	entry.Level = slogLevel(level)
	entry.Summary = summary.String
	entry.Err = errText.String
	entry.Watcher = watcher.String
	if data.String != "" {
		entry.Data = []byte(data.String)
	}
	return entry, nil
}
