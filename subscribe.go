package observe

import "sync"

// DefaultTailBuffer is how many entries a subscriber may fall behind by.
//
// Small on purpose. A tail is a live view of what is happening now; a
// subscriber that is thousands of entries behind is not watching a tail, it
// is reading history badly. The store is where history lives.
const DefaultTailBuffer = 64

// subscriber is one live tail.
type subscriber struct {
	entries chan Entry

	// closeOnce guards against the double close that would otherwise be easy
	// to reach: the publisher closes a subscriber that fell behind, and the
	// reader closes it on unsubscribe. Whichever happens second would panic.
	closeOnce sync.Once
}

func (s *subscriber) close() {
	s.closeOnce.Do(func() { close(s.entries) })
}

// Subscribe returns a channel of entries as they are recorded, and a function
// that stops the subscription.
//
//	entries, stop := recorder.Subscribe(0)
//	defer stop()
//	for entry := range entries { … }
//
// A buffer of zero or less uses DefaultTailBuffer.
//
// # A subscriber that falls behind is closed, not starved
//
// When the buffer fills, the subscription is closed and removed. The reader
// sees a closed channel and knows the tail ended; it does not quietly receive
// a sample of what happened. The alternative — dropping the entry and
// carrying on — leaves a reader watching a stream with holes in it and no way
// to tell, which for a debugging tool is the worst of the three outcomes.
// broadcast made the same call for the same reason, and a reconnecting client
// resumes a live tail in one round trip.
//
// A subscriber never blocks a recording. The send is non-blocking, so a
// reader that has stopped reading costs dropped subscriptions rather than
// request latency — the same trade the entry queue makes, and for the same
// reason: this sits on the path of every statement the application runs.
//
// Entries arrive after sampling and after the recorder has filled in the id,
// the time and the tags, so a tail shows exactly what the store will hold.
// They arrive *before* the store writes them, which is what makes the tail
// live rather than a poll of the last flush.
//
// Stop is idempotent.
func (r *Recorder) Subscribe(buffer int) (<-chan Entry, func()) {
	if r == nil {
		// A usable pair rather than nil: a nil *Recorder records nothing, so
		// a tail of it is an empty stream that ends, not a panic.
		empty := make(chan Entry)
		close(empty)
		return empty, func() {}
	}

	if buffer <= 0 {
		buffer = DefaultTailBuffer
	}
	sub := &subscriber{entries: make(chan Entry, buffer)}

	r.subsMu.Lock()
	if r.subs == nil {
		r.subs = make(map[*subscriber]struct{})
	}
	r.subs[sub] = struct{}{}
	r.subsMu.Unlock()

	return sub.entries, func() { r.unsubscribe(sub) }
}

func (r *Recorder) unsubscribe(sub *subscriber) {
	r.subsMu.Lock()
	_, present := r.subs[sub]
	delete(r.subs, sub)
	r.subsMu.Unlock()

	if present {
		sub.close()
	}
}

// Subscribers reports how many live tails are open, for the dashboard and a
// test.
func (r *Recorder) Subscribers() int {
	if r == nil {
		return 0
	}
	r.subsMu.RLock()
	defer r.subsMu.RUnlock()
	return len(r.subs)
}

// publish hands an entry to every live tail.
//
// Called on the recording goroutine, so it does exactly two things that can
// cost time: a read lock and one non-blocking send per subscriber. A
// subscriber that cannot take it is collected and closed after the lock is
// released, because closing under the read lock and then taking the write
// lock to remove it would deadlock.
func (r *Recorder) publish(e Entry) {
	r.subsMu.RLock()
	if len(r.subs) == 0 {
		r.subsMu.RUnlock()
		return
	}
	var behind []*subscriber
	for sub := range r.subs {
		select {
		case sub.entries <- e:
		default:
			behind = append(behind, sub)
		}
	}
	r.subsMu.RUnlock()

	for _, sub := range behind {
		r.unsubscribe(sub)
	}
}
