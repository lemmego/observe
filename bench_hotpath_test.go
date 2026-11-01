// Benchmarks for what instrumentation costs the caller.
//
// These exist because "recording never blocks a request" is true and is not
// the same claim as "recording is free". Record itself never blocks — it is a
// non-blocking send onto a bounded queue — but everything needed to reach it
// runs on the caller's goroutine: redacting and rendering every bound
// parameter, collapsing the statement, and marshalling the payload.
//
// The number that matters is BenchmarkQuerySampledOut. The sampler runs
// inside Record, which is after the caller has already built the Entry, so a
// sampled-out statement pays the full construction cost and is then thrown
// away. At a 1% rate that saves about a third of the work rather than
// ninety-nine percent of it, and the cost is mostly allocation — which is GC
// pressure, which is latency somewhere else.
//
// Run them before and after anything that touches the recording path:
//
//	go test -run XXX -bench BenchmarkQuery -benchtime 200000x .

package observe_test

import (
	"context"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

const stmt = `SELECT p.id, p.title, p.body, p.created_at FROM posts p
  JOIN circle_members m ON m.circle_id = p.circle_id
  WHERE m.user_id = $1 AND p.deleted_at IS NULL ORDER BY p.created_at DESC LIMIT 20`

func args() []any { return []any{7, "ada@example.com", true, 20} }

// What a statement costs when nothing is recording — the floor.
func BenchmarkQueryWithNoRecorder(b *testing.B) {
	observe.SetDefault(nil)
	ctx := context.Background()
	b.ReportAllocs()
	for b.Loop() {
		observe.RecordQuery(ctx, "sqldriver", stmt, args(), 3*time.Millisecond, nil)
	}
}

// What it costs when a sampler is going to throw the entry away anyway.
func BenchmarkQuerySampledOut(b *testing.B) {
	rec := observe.New(memory.New(),
		observe.WithRequestSampler(func(string) bool { return false }))
	observe.SetDefault(rec)
	b.Cleanup(func() { observe.SetDefault(nil); _ = rec.Close(context.Background()) })

	// The decision cached on the context, as the HTTP middleware caches it.
	id := observe.NewID()
	ctx := observe.WithSampling(
		observe.WithRequestID(context.Background(), id), rec.ShouldSampleRequest(id))
	b.ReportAllocs()
	for b.Loop() {
		observe.RecordQuery(ctx, "sqldriver", stmt, args(), 3*time.Millisecond, nil)
	}
}

// And when it is kept.
func BenchmarkQueryRecorded(b *testing.B) {
	rec := observe.New(memory.New(), observe.WithQueueSize(1<<16))
	observe.SetDefault(rec)
	b.Cleanup(func() { observe.SetDefault(nil); _ = rec.Close(context.Background()) })

	ctx := observe.WithRequestID(context.Background(), observe.NewID())
	b.ReportAllocs()
	for b.Loop() {
		observe.RecordQuery(ctx, "sqldriver", stmt, args(), 3*time.Millisecond, nil)
	}
}

// And what the production profile saves on a statement that is kept, which
// is the other half: sampling helps the ones you throw away, this helps the
// ones you do not.
func BenchmarkQueryRecordedInProduction(b *testing.B) {
	rec := observe.New(memory.New(),
		observe.WithQueueSize(1<<16),
		observe.WithProfile(observe.ProfileProduction))
	observe.SetDefault(rec)
	b.Cleanup(func() { observe.SetDefault(nil); _ = rec.Close(context.Background()) })

	ctx := observe.WithRequestID(context.Background(), observe.NewID())
	b.ReportAllocs()
	for b.Loop() {
		observe.RecordQuery(ctx, "sqldriver", stmt, args(), 3*time.Millisecond, nil)
	}
}

// WithoutArgs on its own, so the two halves of the production profile can be
// told apart rather than argued about together.
func BenchmarkQueryRecordedWithoutArgs(b *testing.B) {
	rec := observe.New(memory.New(), observe.WithQueueSize(1<<16), observe.WithoutArgs())
	observe.SetDefault(rec)
	b.Cleanup(func() { observe.SetDefault(nil); _ = rec.Close(context.Background()) })

	ctx := observe.WithRequestID(context.Background(), observe.NewID())
	b.ReportAllocs()
	for b.Loop() {
		observe.RecordQuery(ctx, "sqldriver", stmt, args(), 3*time.Millisecond, nil)
	}
}

// And the normaliser alone, which is the one that costs rather than saves on
// this path: it buys volume downstream, not time here.
func BenchmarkNormaliseSQL(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = observe.NormaliseSQL(stmt)
	}
}
