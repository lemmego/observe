package provider

// MigrationStub is the migration published into a project.
//
// The schema is spelled out here rather than delegated back to the sqlstore
// package. That is the point of publishing it: the file becomes the
// project's, so a column can be added, a type changed, or the retention
// strategy replaced with a partition. A stub that called SchemaStatements
// would look like ownership and give none of it.
//
// The cost is that this and sqlstore.SchemaStatements can drift, which
// TestThePublishedMigrationMatchesTheSchema pins.
//
// DateTime rather than Timestamp: MySQL's TIMESTAMP tops out in 2038 and,
// with no explicit default, acquires an automatic ON UPDATE CURRENT_TIMESTAMP
// that would rewrite the moment an entry was recorded — which is the one
// column here that must never move.
//
// Its precision has to come from the dialect, which is why the stub carries a
// helper rather than a literal 6. SQLite stores no types: a driver decides
// what Go value a column yields from the type it was declared with, and
// glebarez/go-sqlite maps DATETIME to time.Time while DATETIME(6) — a
// spelling it does not recognise — comes back a string. Every read here
// orders by this column and scans it into a time.Time, so a precision on
// SQLite makes the table writable and unreadable: entries record cleanly and
// the dashboard, the JSON API and observe:last all answer "entries could not
// be read". MySQL needs the 6, so dropping it everywhere is not the fix — a
// bare DATETIME there truncates to the second, and a request issuing twenty
// statements inside one second would come back in an arbitrary order.
//
// sqlstore.SchemaStatements has always had this right, which is why nothing
// here caught it: storetest builds its tables with that, and only a
// published project runs this.
const MigrationStub = `package migrations

import (
	"database/sql"
	"os"
	"strings"

	"github.com/lemmego/migration"
)

func init() {
	migration.GetMigrator().AddMigration(&migration.Migration{
		Version: "20261028000000",
		Up:      mig_20261028000000_create_observe_entries_table_up,
		Down:    mig_20261028000000_create_observe_entries_table_down,
	})
}

// Where lemmego/observe keeps what it records. This file belongs to your
// project: change a column, add one, or swap the indexes for a partitioned
// table if the volume warrants it. Nothing in the observe package reads this
// file back, so an edit here is yours to keep.
//
// What observe does expect is that the columns it reads and writes exist with
// compatible types. The three indexes are not decoration: the first serves
// the only question this table exists to answer, and the second is what the
// list view's cursor pages on.
//
// This table grows with traffic and nothing prunes it on its own. Run
// ` + "`lemmego run observe:prune`" + ` on a schedule, or give it a retention
// policy in your database.
// recordedAtPrecision is the fractional-second precision of recorded_at.
//
// It differs by dialect and must: SQLite reads a column's Go type from its
// declared type, and its driver maps DATETIME to a time.Time while
// DATETIME(6) comes back a string — so a precision there makes this table
// writable and unreadable. MySQL needs the 6, because a bare DATETIME
// truncates to the second and a request's entries are ordered by this column.
func recordedAtPrecision() uint {
	// DB_DRIVER is what lemmego/migration reads to choose a dialect,
	// defaulting to sqlite when unset, so this agrees with the DDL being
	// generated around it rather than deciding separately.
	driver := strings.ToLower(strings.TrimSpace(os.Getenv("DB_DRIVER")))
	if driver == "" || strings.HasPrefix(driver, "sqlite") {
		return 0
	}
	return 6
}

func mig_20261028000000_create_observe_entries_table_up(tx *sql.Tx) error {
	if _, err := tx.Exec(migration.Create("observe_entries", func(t *migration.Table) {
		t.String("id", 64).Primary()
		t.String("kind", 32)
		t.String("request_id", 64).Default("")
		t.DateTime("recorded_at", recordedAtPrecision())
		t.BigInt("duration_ns").Default(0)
		t.Int("level").Default(0)
		t.Text("summary")
		t.Text("data").Nullable()
		t.Text("error")
		t.String("watcher", 64).Default("")
	}).Build()); err != nil {
		return err
	}

	// Raw rather than t.Index, because the DSL names a multi-column index
	// after its columns alone — and index names are schema-global on Postgres
	// and database-global on SQLite, so two tables each wanting an index on
	// (request_id, recorded_at) would collide and the second would fail the
	// batch.
	for _, statement := range []string{
		"CREATE INDEX idx_observe_entries_request ON observe_entries (request_id, recorded_at)",
		"CREATE INDEX idx_observe_entries_recorded ON observe_entries (recorded_at, id)",
		"CREATE INDEX idx_observe_entries_kind ON observe_entries (kind, recorded_at)",
	} {
		if _, err := tx.Exec(statement); err != nil {
			return err
		}
	}

	return nil
}

func mig_20261028000000_create_observe_entries_table_down(tx *sql.Tx) error {
	if _, err := tx.Exec("DROP TABLE IF EXISTS observe_entries"); err != nil {
		return err
	}
	return nil
}
`

// TagsMigrationStub adds the tags column to an existing table.
//
// A second migration rather than an edit to the first, because the first has
// already run everywhere it was going to run. MigrationVersion is a fixed
// constant precisely so a republish is a no-op, which means editing the stub
// changes nothing for an existing project and would silently leave its table
// a column short of what the store writes — a failure that lands on the first
// entry, from inside somebody's request.
//
// A project publishing for the first time gets both files and applies them in
// order, which is one redundant ALTER on a table that was created moments
// before. That is the cost of the arrangement and it is paid once.
//
// One ALTER for all three dialects: Postgres, MySQL and SQLite all accept
// ADD COLUMN with a NOT NULL default, which is what makes the backfill of
// existing rows the database's problem rather than this file's.
const TagsMigrationStub = `package migrations

import (
	"database/sql"

	"github.com/lemmego/migration"
)

func init() {
	migration.GetMigrator().AddMigration(&migration.Migration{
		Version: "20261102000000",
		Up:      mig_20261102000000_add_tags_to_observe_entries_up,
		Down:    mig_20261102000000_add_tags_to_observe_entries_down,
	})
}

// Tags label an entry for retrieval a request id cannot serve — "everything
// this user did", "everything that failed" — which is a question no
// correlation id can answer, because every request has a different one.
//
// There is no index on this column and that is deliberate: it is matched with
// LIKE, which no btree can serve. Retention is what bounds the scan. This
// file is yours, so if the volume warrants it, change the column to something
// indexable — a Postgres text[] with a GIN index, or a join table — and the
// store will keep writing the packed string either way.
func mig_20261102000000_add_tags_to_observe_entries_up(tx *sql.Tx) error {
	if _, err := tx.Exec(
		"ALTER TABLE observe_entries ADD COLUMN tags VARCHAR(1024) NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}
	return nil
}

func mig_20261102000000_add_tags_to_observe_entries_down(tx *sql.Tx) error {
	if _, err := tx.Exec("ALTER TABLE observe_entries DROP COLUMN tags"); err != nil {
		return err
	}
	return nil
}
`

// SourceMigrationStub adds the source column.
//
// A third file rather than an edit to either of the first two, for the
// reason the tags migration is a second one: a fixed MigrationVersion makes
// a republish a no-op, so editing a stub that has already run changes
// nothing for an existing project and leaves its table a column short of
// what the store writes.
const SourceMigrationStub = `package migrations

import (
	"database/sql"

	"github.com/lemmego/migration"
)

func init() {
	migration.GetMigrator().AddMigration(&migration.Migration{
		Version: "20261103000000",
		Up:      mig_20261103000000_add_source_to_observe_entries_up,
		Down:    mig_20261103000000_add_source_to_observe_entries_down,
	})
}

// Which process recorded an entry, as service/host/instance.
//
// A shared table across several web processes is the normal deployment, and
// without this the dashboard cannot say which instance served a request —
// two instances' entries are indistinguishable, so "which host is slow" and
// "is it only the workers" have no answer.
//
// There is no index, and the column is matched with LIKE so a prefix selects
// a service and a longer one selects an instance. Retention bounds the scan,
// the same trade the tags column makes. This file is yours: if you query by
// source often enough to care, an index on it is a one-line change here.
func mig_20261103000000_add_source_to_observe_entries_up(tx *sql.Tx) error {
	if _, err := tx.Exec(
		"ALTER TABLE observe_entries ADD COLUMN source VARCHAR(128) NOT NULL DEFAULT ''",
	); err != nil {
		return err
	}
	return nil
}

func mig_20261103000000_add_source_to_observe_entries_down(tx *sql.Tx) error {
	if _, err := tx.Exec("ALTER TABLE observe_entries DROP COLUMN source"); err != nil {
		return err
	}
	return nil
}
`
