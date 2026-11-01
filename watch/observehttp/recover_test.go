package observehttp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observehttp"
)

// recording installs a recorder as the default for the length of one test.
//
// The default is a package-level singleton — it has to be, because a driver
// wrapper runs where nothing can hand it anything — so a test that installs
// one has to take it back down or the next test records into a store that
// belongs to this one.
func recording(t *testing.T) (*observe.Recorder, observe.Store) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store)
	observe.SetDefault(recorder)
	t.Cleanup(func() {
		observe.SetDefault(nil)
		_ = recorder.Close(context.Background())
	})
	return recorder, store
}

// entries drains the recorder and returns what it holds.
//
// Draining first because recording is asynchronous and bounded by design:
// an assertion made the instant the response came back would be racing the
// writer goroutine, and would pass or fail on scheduling.
func entries(t *testing.T, recorder *observe.Recorder, store observe.Store, q observe.Query) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), q)
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

func panicking(v any) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(v) })
}

// TestAPanicIsRecordedWithItsStack is the whole point of the package.
//
// Before it, a panic reached chi's recoverer, which printed a stack to
// stderr and wrote 500. observe could see the 500 — status is observable
// from outside — and nothing whatever about the cause.
func TestAPanicIsRecordedWithItsStack(t *testing.T) {
	recorder, store := recording(t)

	w := httptest.NewRecorder()
	observehttp.Recover()(panicking(errors.New("the database went away"))).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/launch/abc", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("answered %d, want 500", w.Code)
	}

	found := entries(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindException},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d exceptions, want 1", len(found))
	}

	entry := found[0]
	if !strings.Contains(entry.Summary, "the database went away") {
		t.Errorf("summary does not name the panic: %q", entry.Summary)
	}
	if entry.Err != "the database went away" {
		t.Errorf("Err = %q, want the panic's text", entry.Err)
	}

	var payload struct {
		Panic  string `json:"panic"`
		Stack  string `json:"stack"`
		Method string `json:"method"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	// The stack is the reason this entry exists rather than a flag on the
	// request, so an empty one is a failure even with everything else right.
	if !strings.Contains(payload.Stack, "observehttp_test") {
		t.Errorf("the stack does not reach the test that panicked:\n%s", payload.Stack)
	}
	if payload.Method != http.MethodGet || payload.Path != "/launch/abc" {
		t.Errorf("payload does not say which request: %+v", payload)
	}
}

// TestItGroupsUnderTheRequestThatCausedIt is what makes the entry findable.
//
// An exception nobody can get to from the request is a stack trace in a
// different filing cabinet.
func TestItGroupsUnderTheRequestThatCausedIt(t *testing.T) {
	recorder, store := recording(t)

	// Standing in for observe/provider's middleware, which runs outside this
	// one and is where the id is minted.
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r = r.WithContext(observe.WithRequestID(r.Context(), "req-42"))

	observehttp.Recover()(panicking("boom")).ServeHTTP(httptest.NewRecorder(), r)

	found := entries(t, recorder, store, observe.Query{RequestID: "req-42"})
	if len(found) != 1 {
		t.Fatalf("found %d entries under the request, want 1", len(found))
	}
	if found[0].Kind != observe.KindException {
		t.Errorf("kind = %q, want exception", found[0].Kind)
	}
}

// TestACancelledContextStillRecords.
//
// A panic frequently arrives with a context that is already done — a client
// that hung up, a timeout — and that is exactly when somebody wants to know
// what happened. Recording through the request's context unchanged would
// drop the entry in that case.
func TestACancelledContextStillRecords(t *testing.T) {
	recorder, store := recording(t)

	ctx, cancel := context.WithCancel(observe.WithRequestID(context.Background(), "req-gone"))
	cancel()

	r := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
	observehttp.Recover()(panicking("boom")).ServeHTTP(httptest.NewRecorder(), r)

	found := entries(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindException},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d exceptions, want 1 — a cancelled context must "+
			"not lose the entry", len(found))
	}
	if found[0].RequestID != "req-gone" {
		t.Errorf("correlation was lost: RequestID = %q", found[0].RequestID)
	}
}

// TestAnAbortIsNotAnException.
//
// http.ErrAbortHandler is net/http's way of saying "stop, quietly". Treating
// it as a crash would fill the panel with cancelled responses, and writing
// 500 after it would contradict the abort.
func TestAnAbortIsNotAnException(t *testing.T) {
	recorder, store := recording(t)

	var repanicked any
	func() {
		defer func() { repanicked = recover() }()
		observehttp.Recover()(panicking(http.ErrAbortHandler)).
			ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	}()

	if repanicked != http.ErrAbortHandler {
		t.Errorf("ErrAbortHandler was swallowed; recovered %v", repanicked)
	}
	if found := entries(t, recorder, store, observe.Query{}); len(found) != 0 {
		t.Errorf("recorded %d entries for an abort, want none", len(found))
	}
}

// TestAHandlerThatDoesNotPanicIsUntouched.
//
// The middleware sits on every request, so the ordinary path has to be
// exactly the handler's own answer — status, body and all.
func TestAHandlerThatDoesNotPanicIsUntouched(t *testing.T) {
	recorder, store := recording(t)

	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("fine"))
	})

	w := httptest.NewRecorder()
	observehttp.Recover()(ok).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusTeapot || w.Body.String() != "fine" {
		t.Errorf("the handler's answer was altered: %d %q", w.Code, w.Body.String())
	}
	if found := entries(t, recorder, store, observe.Query{}); len(found) != 0 {
		t.Errorf("recorded %d entries for a request that did not panic", len(found))
	}
}

// TestItWorksWithNoRecorderInstalled.
//
// Recording is off by default, and a panic must still be recovered and
// answered when it is. A recoverer that only worked while observe was
// enabled would be worse than the one it replaced.
func TestItWorksWithNoRecorderInstalled(t *testing.T) {
	observe.SetDefault(nil)

	w := httptest.NewRecorder()
	observehttp.Recover()(panicking("boom")).
		ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))

	if w.Code != http.StatusInternalServerError {
		t.Errorf("answered %d with recording off, want 500", w.Code)
	}
}
