package observe

import (
	"hash/fnv"
	"math"
)

// SampleRequests returns a sampler keeping a fraction of requests whole.
//
//	observe.New(store, observe.WithSampler(observe.SampleRequests(0.1)))
//
// Whole requests, not a fraction of entries. The difference is the entire
// point: keeping a tenth of the queries of every request gives you ten
// percent of a story about all of them, where keeping every query of a tenth
// of requests gives you the whole story about some — and the whole story is
// the one you can debug. WithSampler's own documentation says to sample this
// way; this is the function that makes it a one-liner rather than advice.
//
// The decision is a hash of the request id, so it is stable: every entry of
// one request agrees, including entries recorded minutes apart by a worker
// that inherited the id. A random draw per entry would shred exactly the
// grouping this exists to preserve.
//
// A rate of 1 or more keeps everything and a rate of 0 or less keeps nothing.
//
// # An entry with no request id is kept
//
// There is nothing to sample coherently: it belongs to no group, so dropping
// it by the same rate would thin a population rather than select from it. In
// practice these are rare by design — a job gets its own id from the job
// watcher, and an uncorrelated query means a driver implementing only the
// legacy non-context interfaces. If that is not true of your application, the
// honest fix is to find out why entries are uncorrelated rather than to
// sample them away.
func SampleRequests(rate float64) func(Entry) bool {
	switch {
	case rate >= 1:
		return nil
	case rate <= 0:
		return func(Entry) bool { return false }
	}

	// The threshold is compared against a hash spread over the full uint64
	// range, so the comparison is integer and the float is touched once here
	// rather than per entry.
	threshold := uint64(rate * math.MaxUint64)

	return func(e Entry) bool {
		if e.RequestID == "" {
			return true
		}
		hash := fnv.New64a()
		//nolint:errcheck // hash.Write never returns an error.
		_, _ = hash.Write([]byte(e.RequestID))
		return hash.Sum64() < threshold
	}
}
