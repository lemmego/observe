package observejob_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observejob"
	"github.com/lemmego/tasker"
)

type testJob struct{ Name string }

func (j *testJob) Handle(context.Context) error { return nil }

const kind = "observejob.test"

func init() { tasker.RegisterJob(kind, func() tasker.Job { return &testJob{} }) }

// recordingDriver captures what reached the driver.
type recordingDriver struct {
	tasker.Driver
	sent []*tasker.JobRow
	err  error
}

func (d *recordingDriver) Enqueue(_ context.Context, row *tasker.JobRow) error {
	if d.err != nil {
		return d.err
	}
	d.sent = append(d.sent, row)
	return nil
}

// watching installs the watcher and takes the process-wide registrations back
// down, which they need because tasker's middleware registries are global.
func watching(t *testing.T) (*observe.Recorder, observe.Store) {
	t.Helper()
	store := observememory.New()
	recorder := observe.New(store)
	observejob.Watch(recorder)
	t.Cleanup(func() {
		tasker.SetDispatchMiddlewares(nil)
		tasker.SetGlobalMiddlewares(nil)
		_ = recorder.Close(context.Background())
	})
	return recorder, store
}

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindJob},
	})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

func payloadOf(t *testing.T, entry observe.Entry) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(entry.Data, &out); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return out
}

// An enqueue is recorded and correlated to the request that made it. Before
// tasker grew a dispatch hook this was not recordable at all, which meant a
// job queued and never picked up looked exactly like a job nobody queued.
func TestAnEnqueueIsRecordedAgainstTheRequest(t *testing.T) {
	recorder, store := watching(t)

	driver := &recordingDriver{}
	manager := tasker.NewManager(tasker.WithDriver(driver), tasker.WithNodeID("test"))
	ctx := observe.WithRequestID(context.Background(), "req-1")
	if _, err := manager.Dispatch(ctx, &testJob{Name: "one"}); err != nil {
		t.Fatal(err)
	}

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d job entries, want 1", len(found))
	}
	if found[0].RequestID != "req-1" {
		t.Errorf("RequestID = %q, want the queueing request's", found[0].RequestID)
	}
	if got := payloadOf(t, found[0])["op"]; got != "queued" {
		t.Errorf("op = %v, want queued", got)
	}
}

// An enqueue that failed is the one most worth seeing, and the error is only
// known after the driver call — so the entry has to be built there.
func TestAFailedEnqueueIsRecordedAsAnError(t *testing.T) {
	recorder, store := watching(t)

	driver := &recordingDriver{err: errors.New("the queue is unreachable")}
	manager := tasker.NewManager(tasker.WithDriver(driver), tasker.WithNodeID("test"))
	if _, err := manager.Dispatch(context.Background(), &testJob{Name: "one"}); err == nil {
		t.Fatal("the error was swallowed")
	}

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d job entries, want 1", len(found))
	}
	if found[0].Level < slog.LevelError {
		t.Errorf("level = %v, want error", found[0].Level)
	}
	if found[0].Err == "" {
		t.Error("the enqueue error did not reach the entry")
	}
}

// TestAnExecutionNamesTheRequestThatQueuedIt.
//
// The two halves run in different processes, so they cannot share a
// correlation id — the execution gets its own, and the link is a field. This
// is the assertion that "this request queued three jobs, and here they are"
// is answerable at all.
func TestAnExecutionNamesTheRequestThatQueuedIt(t *testing.T) {
	recorder, store := watching(t)

	driver := &recordingDriver{}
	manager := tasker.NewManager(tasker.WithDriver(driver), tasker.WithNodeID("test"))
	ctx := observe.WithRequestID(context.Background(), "req-2")
	row, err := manager.Dispatch(ctx, &testJob{Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if row.Metadata[observejob.MetadataKey] != "req-2" {
		t.Fatalf("the row was not stamped: %v", row.Metadata)
	}

	// Executed the way a worker does, in a context that knows nothing about
	// the request — which is the point.
	runOne(t, manager, row)

	found := drain(t, recorder, store)
	var ran observe.Entry
	for _, entry := range found {
		if payloadOf(t, entry)["op"] == "ran" {
			ran = entry
		}
	}
	if ran.ID == "" {
		t.Fatal("no execution entry was recorded")
	}
	if ran.RequestID == "req-2" {
		t.Error("the execution reused the queueing request's id; a worker's " +
			"queries would then file under a web request that finished minutes ago")
	}
	if ran.RequestID == "" {
		t.Error("the execution has no correlation id of its own, so nothing it did groups under it")
	}
	if got := payloadOf(t, ran)["queued_by"]; got != "req-2" {
		t.Errorf("queued_by = %v, want the queueing request's id", got)
	}
}

// A job queued outside a request — by the scheduler, by a command — has
// nothing to link to, and must not invent one.
func TestAJobQueuedOutsideARequestHasNoLink(t *testing.T) {
	recorder, store := watching(t)

	driver := &recordingDriver{}
	manager := tasker.NewManager(tasker.WithDriver(driver), tasker.WithNodeID("test"))
	row, err := manager.Dispatch(context.Background(), &testJob{Name: "one"})
	if err != nil {
		t.Fatal(err)
	}
	if _, stamped := row.Metadata[observejob.MetadataKey]; stamped {
		t.Errorf("the row was stamped with nothing: %v", row.Metadata)
	}

	runOne(t, manager, row)

	found := drain(t, recorder, store)
	for _, entry := range found {
		payload := payloadOf(t, entry)
		if payload["op"] == "ran" {
			if _, present := payload["queued_by"]; present {
				t.Errorf("queued_by = %v for a job nobody queued in a request", payload["queued_by"])
			}
		}
	}
}

// runOne runs a row through the registered middleware chain, installing the
// row on the context the way the worker does.
//
// It calls tasker.ContextWithRow itself, because the worker's processJob is
// unexported. That the *worker* installs it before the chain runs is asserted
// on tasker's side, against the real worker; what is asserted here is that
// this package reads it. Splitting it that way is deliberate — a test that
// stubs both halves proves neither.
func runOne(t *testing.T, manager *tasker.Manager, row *tasker.JobRow) {
	t.Helper()
	row.Kind = kind
	chain := tasker.GetGlobalMiddlewares()
	next := func(ctx context.Context) error { return nil }
	for i := len(chain) - 1; i >= 0; i-- {
		mw, inner := chain[i], next
		next = func(ctx context.Context) error {
			return mw(ctx, &testJob{}, row.Payload, inner)
		}
	}
	if err := next(tasker.ContextWithRow(context.Background(), row)); err != nil {
		t.Fatalf("running: %v", err)
	}
}
