// Package storetest is the contract every observe Store must satisfy.
//
// Two implementations held to one contract is the only cheap way to know that
// the memory store a test suite runs against behaves like the database an
// operator actually reads. Run it from a store's own test file:
//
//	func TestMemoryStoreSatisfiesTheContract(t *testing.T) {
//	    storetest.Run(t, func(*testing.T) observe.Store {
//	        return memory.New()
//	    })
//	}
//
// It is exported so a store written outside this module can prove itself too.
package storetest

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/lemmego/observe"
)

// Factory returns a fresh, empty store.
type Factory func(t *testing.T) observe.Store

// Run executes the whole contract.
func Run(t *testing.T, newStore Factory) {
	t.Helper()

	cases := []struct {
		name string
		run  func(t *testing.T, store observe.Store)
	}{
		{"an entry round-trips", anEntryRoundTrips},
		{"a store assigns a missing id", aStoreAssignsAMissingID},
		{"a store keeps an id it was given", aStoreKeepsAnIDItWasGiven},
		{"entries come back newest first", entriesComeBackNewestFirst},
		{"a request id selects only its own entries", aRequestIDSelectsOnlyItsOwn},
		{"a kind filter selects only that kind", aKindFilterSelectsOnlyThatKind},
		{"a level filter selects at or above", aLevelFilterSelectsAtOrAbove},
		{"a time window excludes what is outside it", aTimeWindowExcludes},
		{"contains matches the summary", containsMatchesTheSummary},
		{"the limit is honoured and clamped", theLimitIsHonouredAndClamped},
		{"the cursor pages without repeating", theCursorPagesWithoutRepeating},
		{"an unknown id is ErrNoEntry", anUnknownIDIsErrNoEntry},
		{"prune drops what is older and reports how many", pruneDropsWhatIsOlder},
		{"the payload round-trips byte-identically", thePayloadRoundTripsByteIdentically},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh store per case, so no case depends on another's leftovers.
			tc.run(t, newStore(t))
		})
	}
}

func anEntryRoundTrips(t *testing.T, store observe.Store) {
	ctx := context.Background()
	want := observe.Entry{
		ID:        "entry-1",
		Kind:      observe.KindQuery,
		RequestID: "req-1",
		At:        time.Now().Truncate(time.Millisecond),
		Duration:  3 * time.Millisecond,
		Level:     slog.LevelInfo,
		Summary:   "SELECT 1",
		Watcher:   "sqldriver",
	}
	if err := store.Record(ctx, []observe.Entry{want}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := store.Entry(ctx, "entry-1")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if got.Kind != want.Kind || got.RequestID != want.RequestID ||
		got.Summary != want.Summary || got.Watcher != want.Watcher {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.Duration != want.Duration {
		t.Errorf("Duration = %v, want %v", got.Duration, want.Duration)
	}
	if !got.At.Equal(want.At) {
		t.Errorf("At = %v, want %v", got.At, want.At)
	}
}

func aStoreAssignsAMissingID(t *testing.T, store observe.Store) {
	ctx := context.Background()
	if err := store.Record(ctx, []observe.Entry{{Kind: observe.KindQuery, Summary: "SELECT 1"}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	entries, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].ID == "" {
		t.Error("the store did not assign an id, so the entry cannot be fetched or paged past")
	}
}

func aStoreKeepsAnIDItWasGiven(t *testing.T, store observe.Store) {
	ctx := context.Background()
	if err := store.Record(ctx, []observe.Entry{{ID: "mine", Kind: observe.KindLog}}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := store.Entry(ctx, "mine"); err != nil {
		t.Errorf("the store replaced an id it was given: %v", err)
	}
}

func entriesComeBackNewestFirst(t *testing.T, store observe.Store) {
	ctx := context.Background()
	base := time.Now().Add(-time.Hour)
	record(t, store, []observe.Entry{
		{ID: "a", Kind: observe.KindQuery, At: base, Summary: "first"},
		{ID: "b", Kind: observe.KindQuery, At: base.Add(time.Minute), Summary: "second"},
		{ID: "c", Kind: observe.KindQuery, At: base.Add(2 * time.Minute), Summary: "third"},
	})

	entries, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}
	if entries[0].Summary != "third" || entries[2].Summary != "first" {
		t.Errorf("order was %q, %q, %q; want newest first",
			entries[0].Summary, entries[1].Summary, entries[2].Summary)
	}
}

func aRequestIDSelectsOnlyItsOwn(t *testing.T, store observe.Store) {
	record(t, store, []observe.Entry{
		{ID: "a", Kind: observe.KindQuery, RequestID: "req-1"},
		{ID: "b", Kind: observe.KindQuery, RequestID: "req-2"},
		{ID: "c", Kind: observe.KindQuery, RequestID: "req-1"},
		{ID: "d", Kind: observe.KindQuery},
	})

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: "req-1"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries for req-1, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.RequestID != "req-1" {
			t.Errorf("an entry for %q came back under req-1", entry.RequestID)
		}
	}
}

func aKindFilterSelectsOnlyThatKind(t *testing.T, store observe.Store) {
	record(t, store, []observe.Entry{
		{ID: "a", Kind: observe.KindQuery},
		{ID: "b", Kind: observe.KindLog},
		{ID: "c", Kind: observe.KindCache},
	})

	entries, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindQuery, observe.KindCache},
	})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Kind == observe.KindLog {
			t.Error("a log entry came back from a query+cache filter")
		}
	}
}

func aLevelFilterSelectsAtOrAbove(t *testing.T, store observe.Store) {
	record(t, store, []observe.Entry{
		{ID: "a", Kind: observe.KindLog, Level: slog.LevelDebug},
		{ID: "b", Kind: observe.KindLog, Level: slog.LevelInfo},
		{ID: "c", Kind: observe.KindLog, Level: slog.LevelWarn},
		{ID: "d", Kind: observe.KindLog, Level: slog.LevelError},
	})

	warn := slog.LevelWarn
	entries, err := store.Entries(context.Background(), observe.Query{MinLevel: &warn})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries at or above warn, want 2", len(entries))
	}
	for _, entry := range entries {
		if entry.Level < slog.LevelWarn {
			t.Errorf("an entry at %v came back from a warn filter", entry.Level)
		}
	}
}

func aTimeWindowExcludes(t *testing.T, store observe.Store) {
	now := time.Now()
	record(t, store, []observe.Entry{
		{ID: "old", Kind: observe.KindQuery, At: now.Add(-2 * time.Hour)},
		{ID: "inside", Kind: observe.KindQuery, At: now.Add(-30 * time.Minute)},
		{ID: "future", Kind: observe.KindQuery, At: now.Add(time.Hour)},
	})

	entries, err := store.Entries(context.Background(), observe.Query{
		Since: now.Add(-time.Hour),
		Until: now,
	})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "inside" {
		t.Fatalf("got %d entries %v, want only the one inside the window",
			len(entries), ids(entries))
	}
}

func containsMatchesTheSummary(t *testing.T, store observe.Store) {
	record(t, store, []observe.Entry{
		{ID: "a", Kind: observe.KindQuery, Summary: "SELECT * FROM users"},
		{ID: "b", Kind: observe.KindQuery, Summary: "SELECT * FROM posts"},
	})

	entries, err := store.Entries(context.Background(), observe.Query{Contains: "users"})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "a" {
		t.Fatalf("got %v, want only the users statement", ids(entries))
	}
}

func theLimitIsHonouredAndClamped(t *testing.T, store observe.Store) {
	entries := make([]observe.Entry, 0, 5)
	for i := range 5 {
		entries = append(entries, observe.Entry{
			ID:   "entry-" + itoa(i),
			Kind: observe.KindQuery,
			At:   time.Now().Add(time.Duration(i) * time.Second),
		})
	}
	record(t, store, entries)

	got, err := store.Entries(context.Background(), observe.Query{
		Cursor: observe.Cursor{Limit: 2},
	})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d entries, want the limit of 2 honoured", len(got))
	}

	// A limit from a query string is the normal case, so an absurd one must be
	// clamped rather than served.
	got, err = store.Entries(context.Background(), observe.Query{
		Cursor: observe.Cursor{Limit: 10_000_000},
	})
	if err != nil {
		t.Fatalf("Entries with an absurd limit: %v", err)
	}
	if len(got) > observe.MaxPageSize {
		t.Errorf("got %d entries for a limit of ten million; it was not clamped", len(got))
	}
}

func theCursorPagesWithoutRepeating(t *testing.T, store observe.Store) {
	entries := make([]observe.Entry, 0, 6)
	base := time.Now().Add(-time.Hour)
	for i := range 6 {
		entries = append(entries, observe.Entry{
			ID:   "entry-" + itoa(i),
			Kind: observe.KindQuery,
			At:   base.Add(time.Duration(i) * time.Minute),
		})
	}
	record(t, store, entries)

	ctx := context.Background()
	first, err := store.Entries(ctx, observe.Query{Cursor: observe.Cursor{Limit: 3}})
	if err != nil {
		t.Fatalf("first page: %v", err)
	}
	if len(first) != 3 {
		t.Fatalf("first page has %d entries, want 3", len(first))
	}

	second, err := store.Entries(ctx, observe.Query{
		Cursor: observe.Cursor{Limit: 3, BeforeID: first[len(first)-1].ID},
	})
	if err != nil {
		t.Fatalf("second page: %v", err)
	}
	if len(second) != 3 {
		t.Fatalf("second page has %d entries, want 3", len(second))
	}

	// The cursor is exclusive. An inclusive one shows a duplicate at every
	// page boundary, which is the bug a reader notices and cannot explain.
	seen := make(map[string]bool, len(first))
	for _, entry := range first {
		seen[entry.ID] = true
	}
	for _, entry := range second {
		if seen[entry.ID] {
			t.Errorf("%s appeared on both pages; the cursor is inclusive", entry.ID)
		}
	}
}

func anUnknownIDIsErrNoEntry(t *testing.T, store observe.Store) {
	_, err := store.Entry(context.Background(), "nothing-here")
	if !errors.Is(err, observe.ErrNoEntry) {
		t.Errorf("Entry for an unknown id returned %v, want ErrNoEntry", err)
	}
}

func pruneDropsWhatIsOlder(t *testing.T, store observe.Store) {
	now := time.Now()
	record(t, store, []observe.Entry{
		{ID: "old-1", Kind: observe.KindQuery, At: now.Add(-48 * time.Hour)},
		{ID: "old-2", Kind: observe.KindQuery, At: now.Add(-25 * time.Hour)},
		{ID: "fresh", Kind: observe.KindQuery, At: now.Add(-time.Minute)},
	})

	ctx := context.Background()
	removed, err := store.Prune(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 2 {
		t.Errorf("Prune removed %d, want 2", removed)
	}

	entries, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 1 || entries[0].ID != "fresh" {
		t.Errorf("after pruning, got %v, want only the fresh entry", ids(entries))
	}
}

func thePayloadRoundTripsByteIdentically(t *testing.T, store observe.Store) {
	// The JSON is a contract with the dashboard, so a store that re-marshals
	// it — reordering keys, dropping a zero value — breaks a consumer that
	// never touched the store.
	payload := []byte(`{"sql":"SELECT 1","args":["42"],"rows":-1,"zero":0}`)
	record(t, store, []observe.Entry{
		{ID: "payload", Kind: observe.KindQuery, Data: payload},
	})

	got, err := store.Entry(context.Background(), "payload")
	if err != nil {
		t.Fatalf("Entry: %v", err)
	}
	if string(got.Data) != string(payload) {
		t.Errorf("Data came back as\n  %s\nwant\n  %s", got.Data, payload)
	}
}

func record(t *testing.T, store observe.Store, entries []observe.Entry) {
	t.Helper()
	if err := store.Record(context.Background(), entries); err != nil {
		t.Fatalf("Record: %v", err)
	}
}

func ids(entries []observe.Entry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.ID)
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
