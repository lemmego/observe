package observecache_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/cache"
	cachememory "github.com/lemmego/cache/store/memory"
	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observecache"
)

func newCache(t *testing.T) *cache.Cache {
	t.Helper()
	return cache.New(cachememory.New(cachememory.Config{}))
}

// TestCacheOperationsAreRecorded.
func TestCacheOperationsAreRecorded(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	c := newCache(t)

	observecache.Watch(c, recorder)

	ctx := context.Background()
	if err := c.Put(ctx, "greeting", "hello", time.Minute); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, found, err := c.GetAs[string](ctx, "greeting"); err != nil || !found {
		t.Fatalf("reading it back: found=%v err=%v", found, err)
	}
	if _, found, _ := c.GetAs[string](ctx, "absent"); found {
		t.Fatal("a key that was never written was found")
	}

	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(ctx, observe.Query{
		Kinds:  []observe.Kind{observe.KindCache},
		Cursor: observe.Cursor{Limit: observe.MaxPageSize},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no cache operations were recorded")
	}

	var sawWrite, sawHit bool
	for _, entry := range entries {
		if strings.HasPrefix(entry.Summary, "cache write") {
			sawWrite = true
		}
		if strings.HasPrefix(entry.Summary, "cache hit") {
			sawHit = true
		}
	}
	if !sawWrite {
		t.Error("the write was not recorded")
	}
	if !sawHit {
		t.Error("the hit was not recorded")
	}
}

// TestTheCachedValueIsNotRecorded.
//
// A cached value is whatever the application put there, which includes a
// rendered page with somebody's session in it.
func TestTheCachedValueIsNotRecorded(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	c := newCache(t)
	observecache.Watch(c, recorder)

	ctx := context.Background()
	if err := c.Put(ctx, "page", "<p>session=supersecret</p>", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetAs[string](ctx, "page"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(ctx, observe.Query{Cursor: observe.Cursor{Limit: 50}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(string(entry.Data), "supersecret") {
			t.Errorf("a cached value was recorded: %s", entry.Data)
		}
		if strings.Contains(string(entry.Data), "bytes") && entry.Data == nil {
			t.Error("the size was not recorded either, so nothing useful was kept")
		}
	}
}

// TestCacheEntriesCarryTheRequestsID.
//
// This test used to assert the opposite, under a comment saying that if
// cache.Listener ever took a context this package should carry the id and the
// test should be inverted rather than deleted. cache v0.3.0 did, so it is
// inverted.
//
// It is the whole value of the cache panel: a cache that is being missed
// looks exactly like a cache that is working until the misses can be read
// beside the queries they failed to prevent.
func TestCacheEntriesCarryTheRequestsID(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	c := newCache(t)
	observecache.Watch(c, recorder)

	ctx := observe.WithRequestID(context.Background(), "req-1")
	if err := c.Put(ctx, "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetAs[string](ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.GetAs[string](ctx, "absent"); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindCache},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("recorded %d cache entries for a write, a hit and a miss, want 3", len(entries))
	}
	// Every one of them, because each event is dispatched from a different
	// place in cache and a missed one would be silent — an entry with no
	// request id is indistinguishable from a lookup made outside a request.
	for _, entry := range entries {
		if entry.RequestID != "req-1" {
			t.Errorf("%s has RequestID %q, want the request's", entry.Summary, entry.RequestID)
		}
	}
}

// TestWatchingIsAdditive, so an application's own listener survives.
func TestWatchingIsAdditive(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	c := newCache(t)

	mine := 0
	c.Listen(cache.EventKeyWritten, func(context.Context, cache.Event) { mine++ })
	observecache.Watch(c, recorder)

	if err := c.Put(context.Background(), "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if mine != 1 {
		t.Errorf("the application's own listener fired %d times, want 1 — the "+
			"watcher displaced it", mine)
	}
}
