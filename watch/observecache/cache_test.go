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

// TestCacheEntriesAreUncorrelated states the limitation as a test, so a
// future change to cache.Listener that adds a context is noticed here rather
// than leaving this package quietly dropping an id it could now carry.
func TestCacheEntriesAreUncorrelated(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	c := newCache(t)
	observecache.Watch(c, recorder)

	// Even with a request id in scope, the listener has no context to read it
	// from: cache.Listener is func(Event).
	ctx := observe.WithRequestID(context.Background(), "req-1")
	if err := c.Put(ctx, "k", "v", time.Minute); err != nil {
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
	if len(entries) == 0 {
		t.Fatal("nothing was recorded")
	}
	if entries[0].RequestID != "" {
		t.Errorf("RequestID = %q. If cache.Listener now takes a context, this "+
			"package should carry the id and this test should be inverted "+
			"rather than deleted.", entries[0].RequestID)
	}
}

// TestWatchingIsAdditive, so an application's own listener survives.
func TestWatchingIsAdditive(t *testing.T) {
	store := observememory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	c := newCache(t)

	mine := 0
	c.Listen(cache.EventKeyWritten, func(cache.Event) { mine++ })
	observecache.Watch(c, recorder)

	if err := c.Put(context.Background(), "k", "v", time.Minute); err != nil {
		t.Fatal(err)
	}
	if mine != 1 {
		t.Errorf("the application's own listener fired %d times, want 1 — the "+
			"watcher displaced it", mine)
	}
}
