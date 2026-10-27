package observe_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

// Registering through database/sql rather than calling the wrapper directly,
// because the two prove different things. The forwarding tests prove the
// wrapper implements the interfaces; this proves database/sql's own feature
// detection picks them up, which is the part that fails silently in
// production and cannot be checked by asserting on a method call.
func init() {
	sql.Register("observe-full", observe.Wrap(&fullDriver{log: &callLog{}}))
	sql.Register("observe-bare", observe.Wrap(&bareDriver{log: &callLog{}}))
}

// TestDatabaseSQLRecordsThroughTheWrapper exercises the real path: a
// *sql.DB, a query, an exec, a transaction.
func TestDatabaseSQLRecordsThroughTheWrapper(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	db, err := sql.Open("observe-full", "dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := observe.WithRequestID(context.Background(), "req-integration")

	rows, err := db.QueryContext(ctx, "SELECT id FROM users WHERE id = $1", 42)
	if err != nil {
		t.Fatalf("QueryContext: %v", err)
	}
	if err := rows.Close(); err != nil {
		t.Fatalf("closing rows: %v", err)
	}

	if _, err := db.ExecContext(ctx, "DELETE FROM sessions WHERE id = $1", 7); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE users SET name = $1", "ada"); err != nil {
		t.Fatalf("tx ExecContext: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("closing the recorder: %v", err)
	}

	entries, err := store.Entries(ctx, observe.Query{RequestID: "req-integration"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) < 4 {
		t.Fatalf("got %d entries, want at least the query, the delete, the "+
			"update and the commit:\n%s", len(entries), summaries(entries))
	}

	// Every one has to be correlated: this is the whole point, and every call
	// above was made with a context carrying the request id.
	for _, entry := range entries {
		if entry.RequestID != "req-integration" {
			t.Errorf("%q was recorded uncorrelated", entry.Summary)
		}
	}

	if !containsSummary(entries, "COMMIT") {
		t.Errorf("the transaction's commit was not recorded:\n%s", summaries(entries))
	}
}

// TestBoundArgumentsAreRecordedAndRedacted, because a bound parameter is
// exactly where a password arrives.
func TestBoundArgumentsAreRecordedAndRedacted(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	db, err := sql.Open("observe-full", "dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := observe.WithRequestID(context.Background(), "req-args")
	_, err = db.ExecContext(ctx,
		"UPDATE users SET password_hash = @password WHERE id = @id",
		sql.Named("password", "hunter2"),
		sql.Named("id", 1),
	)
	if err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("closing the recorder: %v", err)
	}

	entries, err := store.Entries(ctx, observe.Query{RequestID: "req-args"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the statement was not recorded")
	}

	payload := string(entries[0].Data)
	if contains(payload, "hunter2") {
		t.Errorf("a password was stored verbatim in the entry payload: %s", payload)
	}
	if !contains(payload, observe.Redacted) {
		t.Errorf("the password argument was not redacted: %s", payload)
	}
	// The id is not a secret and is the whole reason to record arguments.
	if !contains(payload, "1") {
		t.Errorf("a non-secret argument was lost: %s", payload)
	}
}

// TestADriverWithoutFastPathsStillRecords, because database/sql falls back to
// Prepare on ErrSkip and the prepared statement is what then executes. If
// only the fast paths were instrumented, a driver like that would record
// nothing and look like an application making no queries.
func TestADriverWithoutFastPathsStillRecords(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	db, err := sql.Open("observe-bare", "dsn")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx := observe.WithRequestID(context.Background(), "req-bare")
	if _, err := db.ExecContext(ctx, "DELETE FROM t WHERE id = ?", 1); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("closing the recorder: %v", err)
	}

	entries, err := store.Entries(ctx, observe.Query{RequestID: "req-bare"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("a driver with no fast paths recorded nothing; the statement " +
			"executed through Prepare and the prepared statement is not instrumented")
	}
}

func summaries(entries []observe.Entry) string {
	var out string
	for _, entry := range entries {
		out += "  - " + entry.Summary + "\n"
	}
	return out
}

func containsSummary(entries []observe.Entry, want string) bool {
	for _, entry := range entries {
		if entry.Summary == want {
			return true
		}
	}
	return false
}

func contains(haystack, needle string) bool {
	return len(needle) > 0 && len(haystack) >= len(needle) &&
		indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
