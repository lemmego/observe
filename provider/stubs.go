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
// DateTime with a precision of 6 rather than Timestamp: MySQL's TIMESTAMP
// tops out in 2038 and, with no explicit default, acquires an automatic
// ON UPDATE CURRENT_TIMESTAMP that would rewrite the moment an entry was
// recorded — which is the one column here that must never move.
const MigrationStub = `package migrations

import (
	"database/sql"

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
func mig_20261028000000_create_observe_entries_table_up(tx *sql.Tx) error {
	if _, err := tx.Exec(migration.Create("observe_entries", func(t *migration.Table) {
		t.String("id", 64).Primary()
		t.String("kind", 32)
		t.String("request_id", 64).Default("")
		t.DateTime("recorded_at", 6)
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
