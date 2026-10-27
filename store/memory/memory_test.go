package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/storetest"
)

func TestTheMemoryStoreSatisfiesTheContract(t *testing.T) {
	storetest.Run(t, func(*testing.T) observe.Store {
		return memory.New()
	})
}

// TestTheOldestEntriesAreEvicted is what makes this store safe to leave on.
//
// Entries are keyed on request ids and statements, both influenced by
// whoever is sending requests, so an unbounded store here would be a
// memory-exhaustion vector in the component meant to help debug one.
func TestTheOldestEntriesAreEvicted(t *testing.T) {
	store := memory.NewWithCapacity(10)
	ctx := context.Background()

	base := time.Now().Add(-time.Hour)
	for i := range 100 {
		err := store.Record(ctx, []observe.Entry{{
			ID:      "entry-" + itoa(i),
			Kind:    observe.KindQuery,
			At:      base.Add(time.Duration(i) * time.Second),
			Summary: "SELECT " + itoa(i),
		}})
		if err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	if got := store.Len(); got != 10 {
		t.Errorf("the store holds %d entries, want its capacity of 10", got)
	}

	// The newest survive, because the newest are what anybody is debugging.
	if _, err := store.Entry(ctx, "entry-99"); err != nil {
		t.Errorf("the newest entry was evicted: %v", err)
	}
	if _, err := store.Entry(ctx, "entry-0"); err == nil {
		t.Error("the oldest entry survived an eviction of ninety")
	}
}

// TestEvictionDoesNotLeakTheIndex, because a bounded list beside an unbounded
// map is still unbounded.
func TestEvictionDoesNotLeakTheIndex(t *testing.T) {
	store := memory.NewWithCapacity(5)
	ctx := context.Background()

	for i := range 50 {
		if err := store.Record(ctx, []observe.Entry{{
			ID:   "entry-" + itoa(i),
			Kind: observe.KindQuery,
			At:   time.Now(),
		}}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	entries, err := store.Entries(ctx, observe.Query{Cursor: observe.Cursor{Limit: 200}})
	if err != nil {
		t.Fatalf("Entries: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("Entries returned %d, want 5; the index kept rows the list dropped", len(entries))
	}
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
