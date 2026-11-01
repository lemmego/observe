package observeschedule_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observeschedule"
	"github.com/lemmego/tasker"
	"github.com/lemmego/tasker/scheduler"
)

type pruneJob struct{}

func (pruneJob) Handle(context.Context) error { return nil }

func init() { tasker.RegisterJob("observeschedule.test", func() tasker.Job { return pruneJob{} }) }

type stubDriver struct {
	tasker.Driver
	err error
}

func (d *stubDriver) Enqueue(context.Context, *tasker.JobRow) error { return d.err }

func watching(t *testing.T, enqueueErr error) (*observe.Recorder, observe.Store, *scheduler.Scheduler) {
	t.Helper()
	store := observememory.New()
	recorder := observe.New(store)
	sched := scheduler.New(tasker.NewManager(
		tasker.WithDriver(&stubDriver{err: enqueueErr}), tasker.WithNodeID("test")))
	observeschedule.Watch(sched, recorder)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	return recorder, store, sched
}

// fire registers an entry on a one-second schedule and waits for cron to run
// it.
//
// Through the real cron rather than by reaching for dispatchJob, which is
// unexported — and the wait is what buys the test its value: what is being
// asserted is that an entry registered the way an application registers one
// produces a record when it fires.
func fire(t *testing.T, sched *scheduler.Scheduler, store observe.Store) {
	t.Helper()
	if err := sched.Register(scheduler.ScheduledJob{
		ID: "prune-observe-entries", Schedule: "@every 1s",
		Queue: "maintenance", Job: pruneJob{},
	}); err != nil {
		t.Fatal(err)
	}
	if err := sched.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sched.Stop(context.Background()) })

	// Stopped as soon as it has fired once. A one-second schedule left
	// running fires again while the test is reading, which made the count
	// assertions flake — the entry under test is the first one, and a
	// second is noise rather than information.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		found, err := store.Entries(context.Background(), observe.Query{
			Kinds: []observe.Kind{observe.KindSchedule},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(found) > 0 {
			if err := sched.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("the scheduled entry never fired within five seconds")
}

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindSchedule},
	})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

func TestARunIsRecordedAgainstItsRegistration(t *testing.T) {
	recorder, store, sched := watching(t, nil)
	fire(t, sched, store)

	found := drain(t, recorder, store)
	if len(found) == 0 {
		t.Fatal("recorded no runs")
	}
	entry := found[0]
	// Its own correlation id, so anything the run causes groups under it
	// rather than under nothing.
	if entry.RequestID == "" {
		t.Error("the run has no correlation id of its own")
	}

	var payload struct {
		ID       string `json:"id"`
		Schedule string `json:"schedule"`
		Queue    string `json:"queue"`
		Job      string `json:"job"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.ID != "prune-observe-entries" || payload.Schedule != "@every 1s" {
		t.Errorf("payload = %+v, want the registration", payload)
	}
	if payload.Queue != "maintenance" {
		t.Errorf("queue = %q", payload.Queue)
	}
}

// A run that failed to enqueue leaves no job row at all, so without this it
// did not happen as far as the queue is concerned. It is the failure the
// panel exists for.
func TestARunThatFailedToEnqueueIsRecordedAsAnError(t *testing.T) {
	recorder, store, sched := watching(t, errors.New("the queue is unreachable"))
	fire(t, sched, store)

	found := drain(t, recorder, store)
	if len(found) == 0 {
		t.Fatal("recorded no runs — a run that failed is the one worth seeing")
	}
	if found[0].Level < slog.LevelError {
		t.Errorf("level = %v, want error", found[0].Level)
	}
	if found[0].Err == "" {
		t.Error("the dispatch error did not reach the entry")
	}
}
