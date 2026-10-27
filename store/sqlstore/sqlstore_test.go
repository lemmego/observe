package sqlstore_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/glebarez/go-sqlite"
	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/sqlstore"
	"github.com/lemmego/observe/storetest"
)

// newSQLiteStore builds a store over a fresh file-backed SQLite database.
//
// A file rather than :memory:, because each pooled connection to an in-memory
// SQLite gets its own empty database — a suite would then pass having written
// to one connection and read from another.
func newSQLiteStore(t *testing.T) observe.Store {
	t.Helper()

	pool, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "observe.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	if _, err := pool.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatal(err)
	}

	store, err := sqlstore.New(pool, sqlstore.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	return store
}

func TestTheSQLStoreSatisfiesTheContract(t *testing.T) {
	storetest.Run(t, newSQLiteStore)
}

// TestAnUnsupportedDialectIsRefusedAtConstruction rather than at the first
// write — which would happen from inside somebody's request, long after the
// mistake was made.
func TestAnUnsupportedDialectIsRefusedAtConstruction(t *testing.T) {
	pool, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "x.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })

	if _, err := sqlstore.New(pool, "oracle"); err == nil {
		t.Error("an unsupported dialect was accepted; the SQL it generates " +
			"would be refused at the first entry rather than at boot")
	}
	if _, err := sqlstore.New(nil, sqlstore.SQLite); err == nil {
		t.Error("a nil pool was accepted")
	}
}

// TestRecordingThroughAWatchedDatabaseTerminates is the recursion guard,
// exercised the way it actually fails.
//
// This store writes entries through a pool the driver wrapper is watching, so
// without the guard storing an entry produces an entry, which produces a
// write, which produces an entry. Nothing downstream stops it: log.Fanout
// keeps delivering after a failure, so a looping recorder is not contained the
// way a panicking one is.
//
// Asserting that the entry was recorded would NOT catch it — a loop records
// the entry too, and then keeps going. So this asserts the total converges.
//
// The guard is applied twice, by Recorder.flush and again by Store.Record,
// and either alone is sufficient. So removing just one leaves this test
// green: it fails only when both go, at which point one entry becomes
// twenty-one and climbing. That is worth knowing before deleting either as
// redundant — the second is there because a store reached directly, without
// a recorder in front of it, has no other protection.
func TestRecordingThroughAWatchedDatabaseTerminates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "watched.sqlite")

	// Take the real driver from a throwaway pool and register a wrapped copy,
	// which is exactly how an application installs this.
	plain, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	wrappedName := "sqlite-observed-" + t.Name()
	sql.Register(wrappedName, observe.Wrap(plain.Driver()))
	if err := plain.Close(); err != nil {
		t.Fatal(err)
	}

	pool, err := sql.Open(wrappedName, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	if _, err := pool.Exec(`PRAGMA busy_timeout = 5000`); err != nil {
		t.Fatal(err)
	}

	store, err := sqlstore.New(pool, sqlstore.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		t.Fatalf("migrating: %v", err)
	}

	recorder := observe.New(store, observe.WithFlushInterval(10*time.Millisecond))
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	// One entry in. If the guard is missing, storing it is a watched INSERT,
	// which records another, and the count climbs without bound.
	recorder.Record(context.Background(), observe.Entry{
		Kind:    observe.KindQuery,
		Summary: "SELECT 1",
		Watcher: "test",
	})

	// Give a loop time to run away before measuring.
	time.Sleep(250 * time.Millisecond)
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{
		Cursor: observe.Cursor{Limit: observe.MaxPageSize},
	})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries from recording one; the store's own writes "+
			"are being recorded, so recording does not terminate", len(entries))
	}
	if got := recorder.Stats().Recorded; got != 1 {
		t.Errorf("the recorder accepted %d entries, want 1", got)
	}
}

// TestTimestampsSurviveAZoneChange.
//
// A naive column carries no offset, so writing local time and reading it back
// shifts every timestamp by the machine's zone — which would put a request's
// queries before the request that issued them.
func TestTimestampsSurviveAZoneChange(t *testing.T) {
	store := newSQLiteStore(t)
	ctx := context.Background()

	// A zone well away from UTC, so a dropped offset is unmistakable.
	kolkata, err := time.LoadLocation("Asia/Kolkata")
	if err != nil {
		t.Skipf("no zone database on this machine: %v", err)
	}
	at := time.Date(2026, 10, 28, 14, 30, 0, 0, kolkata)

	if err := store.Record(ctx, []observe.Entry{
		{ID: "zoned", Kind: observe.KindQuery, At: at, Summary: "SELECT 1"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Entry(ctx, "zoned")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if !got.At.Equal(at) {
		t.Errorf("At came back as %s, want the same instant as %s (a difference "+
			"of %s)", got.At, at, got.At.Sub(at))
	}
}

// TestSchemaStatementsCoverEveryDialect, because a dialect accepted by New
// and unsupported by the schema fails at migration time in production only.
func TestSchemaStatementsCoverEveryDialect(t *testing.T) {
	for _, dialect := range []sqlstore.Dialect{sqlstore.Postgres, sqlstore.MySQL, sqlstore.SQLite} {
		statements := sqlstore.SchemaStatements(dialect, "")
		if len(statements) < 4 {
			t.Errorf("%s produced %d statements, want the table and its three indexes",
				dialect, len(statements))
		}
		// Index names are database-global on SQLite and schema-global on
		// Postgres, so an index name without the table in it collides with
		// the next table that wants the same column.
		for _, statement := range statements[1:] {
			if !containsAll(statement, "idx_", sqlstore.DefaultTable) {
				t.Errorf("%s: an index name does not carry the table name, so "+
					"it will collide: %s", dialect, statement)
			}
		}
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !contains(haystack, needle) {
			return false
		}
	}
	return true
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
