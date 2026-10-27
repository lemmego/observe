// Package observecache records cache operations.
//
// # These entries are not correlated to a request, and cannot be
//
// cache.Listener is func(Event) — no context.Context. So although the
// listener runs synchronously on the goroutine that caused the operation,
// there is nowhere to read a request id from, and a cache entry therefore
// carries none.
//
// That is worth knowing before reading a request view and wondering where the
// cache went. Hit and miss rates and what is being written are still
// legible in the entry list; "which cache lookups did this request make" is
// not, and will not be until cache.Listener takes a context.
//
// Install it after the cache provider, which is what owns the *cache.Cache:
//
//	observecache.Watch(cache.FromApp(a), recorder)
//
// Through Listen rather than the provider's app.Dispatch bridge, which is
// gated on cache.events being configured on, is a single-slot sink, and —
// since app.Dispatch also takes no context — would lose nothing but gain
// nothing either.
package observecache

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/lemmego/cache"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "cache"

// Watch records every cache event onto the recorder.
//
// Additive: cache.Listen allows several listeners per event, so this does not
// displace an application's own.
func Watch(c *cache.Cache, recorder *observe.Recorder) {
	if c == nil || recorder == nil {
		return
	}

	for name, describe := range map[string]func(cache.Event) (string, any){
		cache.EventHit:          describeHit,
		cache.EventMissed:       describeMissed,
		cache.EventKeyWritten:   describeWritten,
		cache.EventKeyForgotten: describeForgotten,
		cache.EventFlushed:      describeFlushed,
	} {
		// Captured per iteration, or every listener would describe whatever
		// the last one was.
		summarise := describe
		c.Listen(name, func(event cache.Event) {
			summary, payload := summarise(event)
			entry := observe.Entry{
				Kind:    observe.KindCache,
				Level:   slog.LevelInfo,
				Summary: summary,
				Watcher: Watcher,
			}
			if data, err := json.Marshal(payload); err == nil {
				entry.Data = data
			}
			// context.Background rather than a request's: there is no context
			// on this seam. See the package documentation.
			recorder.Record(context.Background(), entry)
		})
	}

	recorder.Watching(Watcher)
}

type payload struct {
	Op    string   `json:"op"`
	Key   string   `json:"key,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	Bytes int      `json:"bytes,omitempty"`
	TTL   string   `json:"ttl,omitempty"`
}

func describeHit(event cache.Event) (string, any) {
	hit, ok := event.(cache.CacheHit)
	if !ok {
		return "cache hit", payload{Op: "hit"}
	}
	// The key and the size, never the value. A cached value is whatever the
	// application put there, which includes a rendered page with somebody's
	// session in it.
	return "cache hit " + hit.Key, payload{
		Op: "hit", Key: hit.Key, Tags: hit.Tags, Bytes: len(hit.Value),
	}
}

func describeMissed(event cache.Event) (string, any) {
	missed, ok := event.(cache.CacheMissed)
	if !ok {
		return "cache miss", payload{Op: "miss"}
	}
	return "cache miss " + missed.Key, payload{
		Op: "miss", Key: missed.Key, Tags: missed.Tags,
	}
}

func describeWritten(event cache.Event) (string, any) {
	written, ok := event.(cache.KeyWritten)
	if !ok {
		return "cache write", payload{Op: "write"}
	}
	out := payload{
		Op: "write", Key: written.Key, Tags: written.Tags, Bytes: len(written.Value),
	}
	if written.TTL > 0 {
		out.TTL = written.TTL.String()
	} else {
		// Said rather than left blank, because "forever" is the answer
		// somebody is usually surprised by.
		out.TTL = "forever"
	}
	return "cache write " + written.Key, out
}

func describeForgotten(event cache.Event) (string, any) {
	forgotten, ok := event.(cache.KeyForgotten)
	if !ok {
		return "cache forget", payload{Op: "forget"}
	}
	return "cache forget " + forgotten.Key, payload{
		Op: "forget", Key: forgotten.Key, Tags: forgotten.Tags,
	}
}

func describeFlushed(event cache.Event) (string, any) {
	flushed, ok := event.(cache.CacheFlushed)
	if !ok {
		return "cache flush", payload{Op: "flush"}
	}
	if len(flushed.Tags) == 0 {
		return "cache flush (everything)", payload{Op: "flush"}
	}
	return "cache flush (tagged)", payload{Op: "flush", Tags: flushed.Tags}
}
