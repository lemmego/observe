package observe_test

import (
	"context"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

func TestATailReceivesWhatIsRecordedAfterItOpened(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	entries, stop := recorder.Subscribe(0)
	defer stop()

	ctx := observe.WithRequestID(context.Background(), "req-1")
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})

	select {
	case got := <-entries:
		if got.Summary != "SELECT 1" {
			t.Errorf("Summary = %q", got.Summary)
		}
		// The entry arrives already finished: the recorder fills in the id,
		// the time and the tags before publishing, so a tail shows exactly
		// what the store will hold rather than a half-built entry.
		if got.ID == "" || got.At.IsZero() || got.RequestID != "req-1" {
			t.Errorf("the entry arrived unfinished: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived on the tail")
	}
}

// TestASubscriberThatFallsBehindIsClosedRatherThanStarved.
//
// Dropping the entry and carrying on would leave a reader watching a stream
// with holes in it and no way to tell, which for a debugging tool is the
// worst of the three outcomes. A closed channel is a reader that knows.
func TestASubscriberThatFallsBehindIsClosedRatherThanStarved(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	entries, stop := recorder.Subscribe(2)
	defer stop()

	ctx := context.Background()
	for i := 0; i < 20; i++ {
		recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
	}

	// Drain what fitted, then the channel must be closed rather than
	// delivering a thinned sample forever.
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, open := <-entries:
			if !open {
				if recorder.Subscribers() != 0 {
					t.Error("the closed subscriber is still registered, so it " +
						"will be published to for the life of the recorder")
				}
				return
			}
		case <-deadline:
			t.Fatal("the subscriber was neither closed nor drained")
		}
	}
}

// Recording must not wait for a reader. A tail sits on the path of every
// statement the application runs, so a browser tab nobody is looking at
// cannot be allowed to cost request latency.
func TestAStalledTailDoesNotBlockRecording(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	ctx := context.Background()

	// Subscribed and never read.
	_, stop := recorder.Subscribe(1)
	defer stop()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 500; i++ {
			recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "SELECT 1"})
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("recording blocked on a tail that was not being read")
	}

	if err := recorder.Close(ctx); err != nil {
		t.Fatal(err)
	}
	found, err := store.Entries(ctx, observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 {
		t.Error("nothing was stored, so the stall cost entries rather than only subscribers")
	}
}

// These connections are long-lived by construction — a tail is open for as
// long as somebody has the dashboard in a tab — so a recorder that shut down
// without closing them would hang the process on every graceful stop.
func TestShutdownClosesEveryTail(t *testing.T) {
	recorder := observe.New(memory.New())

	first, stopFirst := recorder.Subscribe(0)
	second, stopSecond := recorder.Subscribe(0)
	defer stopFirst()
	defer stopSecond()

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	for name, channel := range map[string]<-chan observe.Entry{
		"first": first, "second": second,
	} {
		select {
		case _, open := <-channel:
			if open {
				t.Errorf("the %s tail delivered an entry after shutdown", name)
			}
		case <-time.After(2 * time.Second):
			t.Errorf("the %s tail was not closed by shutdown", name)
		}
	}
}

// Stop is called by a deferred handler and by the publisher closing a
// subscriber that fell behind. Whichever happens second must not panic.
func TestStoppingTwiceIsSafe(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	_, stop := recorder.Subscribe(0)
	stop()
	stop()

	if recorder.Subscribers() != 0 {
		t.Errorf("Subscribers() = %d after stopping", recorder.Subscribers())
	}
}

// A nil recorder records nothing, so a tail of one is an empty stream that
// ends rather than a panic — the same contract every other method on a nil
// *Recorder keeps.
func TestATailOfANilRecorderEndsRatherThanPanicking(t *testing.T) {
	var recorder *observe.Recorder
	entries, stop := recorder.Subscribe(0)
	defer stop()

	select {
	case _, open := <-entries:
		if open {
			t.Error("a nil recorder delivered an entry")
		}
	case <-time.After(time.Second):
		t.Error("the channel of a nil recorder never closed")
	}
}
