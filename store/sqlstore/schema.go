package sqlstore

import (
	"context"
	"fmt"
	"strings"
)

// SchemaStatements returns the DDL for one dialect.
//
// Exported so the published migration and this package cannot silently
// disagree: a test compares the two, because a stub that drifts from the
// queries run against it fails at the first write rather than at build time.
//
// Dialect traps worth knowing, each of which this schema is shaped around:
//
//   - Index names are database-global on SQLite and schema-global on
//     Postgres, so every index name carries the table name. Two tables each
//     wanting an index on (request_id) would otherwise collide.
//   - MySQL refuses an index on an unbounded TEXT column (error 1170), so
//     every indexed column is VARCHAR with a length.
//   - TIMESTAMP WITH TIME ZONE takes its precision before the zone on
//     Postgres, so it is written out rather than parameterised.
func SchemaStatements(dialect Dialect, table string) []string {
	if table == "" {
		table = DefaultTable
	}

	var timestamp, text string
	switch dialect {
	case Postgres:
		timestamp = "TIMESTAMPTZ NOT NULL"
		text = "TEXT"
	case MySQL:
		// DATETIME(6) rather than TIMESTAMP: MySQL's TIMESTAMP tops out in
		// 2038 and, worse, a TIMESTAMP column with no explicit default gets
		// an automatic ON UPDATE CURRENT_TIMESTAMP, which would rewrite the
		// time an entry was recorded.
		timestamp = "DATETIME(6) NOT NULL"
		text = "LONGTEXT"
	default:
		timestamp = "DATETIME NOT NULL"
		text = "TEXT"
	}

	create := fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
	id VARCHAR(64) NOT NULL PRIMARY KEY,
	kind VARCHAR(32) NOT NULL,
	request_id VARCHAR(64) NOT NULL DEFAULT '',
	recorded_at %s,
	duration_ns BIGINT NOT NULL DEFAULT 0,
	level INTEGER NOT NULL DEFAULT 0,
	summary %s NOT NULL,
	data %s,
	error %s NOT NULL,
	watcher VARCHAR(64) NOT NULL DEFAULT ''
)`, table, timestamp, text, text, text)

	return []string{
		create,

		// The query the whole package exists to answer: everything under one
		// request. Ordered by time so the index serves the ordering too.
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_request ON %s (request_id, recorded_at)", table, table),

		// The list view, and the cursor, which order by (recorded_at, id).
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_recorded ON %s (recorded_at, id)", table, table),

		// Narrowing the list to one kind, which is what the dashboard's tabs
		// and every MCP tool do.
		fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_%s_kind ON %s (kind, recorded_at)", table, table),
	}
}

// Migrate creates the table and its indexes.
//
// For a test and for a store built outside a Lemmego application. An
// application gets its schema from the published migration instead, so the
// table is one it owns — readable, editable, rollbackable, and present in
// schema_migrations where an operator expects to find it. Creating tables at
// boot would put a table in somebody's database that nothing in their project
// mentions, which is why observe/provider does not call this.
func (s *Store) Migrate(ctx context.Context) error {
	ctx = contextWithoutRecording(ctx)
	for _, statement := range SchemaStatements(s.dialect, s.table) {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("observe/sqlstore: %s: %w", firstLine(statement), err)
		}
	}
	return nil
}

func firstLine(statement string) string {
	if index := strings.IndexByte(statement, '\n'); index > 0 {
		return statement[:index]
	}
	return statement
}
