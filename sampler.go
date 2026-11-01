package observe

import (
	"hash/fnv"
	"math"
)

// SampleRequests returns a request sampler keeping a fraction of requests
// whole.
//
//	observe.New(store, observe.WithRequestSampler(observe.SampleRequests(0.1)))
//
// Whole requests, not a fraction of entries. The difference is the entire
// point: keeping a tenth of the queries of every request gives you ten
// percent of a story about all of them, where keeping every query of a tenth
// of requests gives you the whole story about some — and the whole story is
// the one you can debug.
//
// It takes a request id rather than an Entry, and that is what makes
// sampling cheap rather than merely correct. A predicate over an Entry can
// only run once an Entry exists — after every bound parameter has been
// redacted, the statement collapsed and the payload marshalled — so a
// statement about to be discarded paid full price first. An id is available
// before any of that, which is why the decision is made where the id is
// minted and cached on the context.
//
// The decision is a hash, so it is stable: every entry of one request agrees,
// including entries recorded minutes apart by a worker that inherited the id.
// A random draw per entry would shred exactly the grouping this exists to
// preserve.
//
// A rate of 1 or more keeps everything and a rate of 0 or less keeps nothing.
//
// # Work with no request id is kept
//
// An empty id belongs to no group, so dropping it by the same rate would thin
// a population rather than select from it. In practice these are rare by
// design — a job gets its own id from the job watcher, and an uncorrelated
// query means a driver implementing only the legacy non-context interfaces.
// If that is not true of your application, the honest fix is to find out why
// entries are uncorrelated rather than to sample them away.
func SampleRequests(rate float64) func(requestID string) bool {
	switch {
	case rate >= 1:
		return nil
	case rate <= 0:
		return func(string) bool { return false }
	}

	// The threshold is compared against a hash spread over the full uint64
	// range, so the comparison is integer and the float is touched once here
	// rather than per entry.
	threshold := uint64(rate * math.MaxUint64)

	return func(requestID string) bool {
		if requestID == "" {
			return true
		}
		hash := fnv.New64a()
		//nolint:errcheck // hash.Write never returns an error.
		_, _ = hash.Write([]byte(requestID))
		return hash.Sum64() < threshold
	}
}
