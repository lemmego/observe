package provider_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/provider"
	"github.com/lemmego/observe/store/memory"
)

func newRecorder(t *testing.T) (*observe.Recorder, observe.Store) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	return recorder, store
}

// TestTheMiddlewareCorrelatesEverythingUnderOneRequest is the point of it.
func TestTheMiddlewareCorrelatesEverythingUnderOneRequest(t *testing.T) {
	recorder, store := newRecorder(t)

	handler := provider.Middleware(recorder)(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			// Standing in for a query: anything recorded with the request's
			// context has to land under the request.
			recorder.Record(r.Context(), observe.Entry{
				Kind: observe.KindQuery, Summary: "SELECT 1",
			})
			w.WriteHeader(http.StatusCreated)
			//nolint:errcheck // the response body is not the subject.
			_, _ = w.Write([]byte("made"))
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/posts?draft=1", nil))

	requestID := response.Header().Get("X-Request-Id")
	if requestID == "" {
		t.Fatal("no X-Request-Id was returned, so nobody reporting a problem can quote one")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: requestID})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries under the request, want the request and its query", len(entries))
	}

	var request observe.Entry
	for _, entry := range entries {
		if entry.Kind == observe.KindRequest {
			request = entry
		}
	}
	if request.Summary != "POST /posts" {
		t.Errorf("Summary = %q, want POST /posts", request.Summary)
	}

	var payload struct {
		Status int    `json:"status"`
		Query  string `json:"query"`
		Bytes  int64  `json:"bytes"`
	}
	if err := json.Unmarshal(request.Data, &payload); err != nil {
		t.Fatalf("decoding the payload: %v", err)
	}
	if payload.Status != http.StatusCreated {
		t.Errorf("Status = %d, want 201", payload.Status)
	}
	if payload.Query != "draft=1" {
		t.Errorf("Query = %q, want draft=1", payload.Query)
	}
	if payload.Bytes != 4 {
		t.Errorf("Bytes = %d, want 4", payload.Bytes)
	}
}

// TestFlushAndHijackSurviveTheWrapper.
//
// This framework has paid for this twice. api/middleware's responseRecorder
// promoted only three methods, so every event stream answered 500 "streaming
// not supported"; and api/app's tracing writer had a hand-written Hijack with
// the wrong signature, so it never satisfied http.Hijacker at all under a
// comment claiming it did.
func TestFlushAndHijackSurviveTheWrapper(t *testing.T) {
	recorder, _ := newRecorder(t)

	var (
		sawFlusher  bool
		sawHijacker bool
	)
	handler := provider.Middleware(recorder)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			// Both styles, because both exist in the wild:
			// http.ResponseController follows Unwrap, and plenty of code —
			// tasker's dashboard among it — asserts directly.
			_, sawFlusher = w.(http.Flusher)
			_, sawHijacker = w.(http.Hijacker)

			if err := http.NewResponseController(w).Flush(); err != nil {
				t.Errorf("ResponseController.Flush: %v", err)
			}
		}))

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if !sawFlusher {
		t.Error("the wrapper does not satisfy http.Flusher by assertion, so " +
			"every event stream behind it answers 500")
	}
	if !sawHijacker {
		t.Error("the wrapper does not satisfy http.Hijacker by assertion, so " +
			"a WebSocket upgrade behind it answers 501")
	}
}

// TestAPanicIsRecordedAndThenContinues.
//
// The panicking request is the one most worth having recorded, and this
// middleware must not quietly become the recoverer.
func TestAPanicIsRecordedAndThenContinues(t *testing.T) {
	recorder, store := newRecorder(t)

	handler := provider.Middleware(recorder)(http.HandlerFunc(
		func(http.ResponseWriter, *http.Request) {
			panic("boom")
		}))

	var recovered any
	func() {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	}()

	if recovered == nil {
		t.Error("the panic was swallowed; this middleware is not the recoverer")
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries for a panicking request, want it recorded", len(entries))
	}
	if entries[0].Err == "" {
		t.Error("the panic was not recorded on the entry")
	}
}

// TestSecretHeadersAreNotRecorded. A Cookie header is a session, and an
// Authorization header is a credential.
func TestSecretHeadersAreNotRecorded(t *testing.T) {
	recorder, store := newRecorder(t)

	handler := provider.Middleware(recorder)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("Authorization", "Bearer sk_live_abcdef")
	request.Header.Set("Cookie", "session=supersecret")
	request.Header.Set("User-Agent", "curl/8")

	handler.ServeHTTP(httptest.NewRecorder(), request)
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	payload := string(entries[0].Data)
	for _, secret := range []string{"sk_live_abcdef", "supersecret"} {
		if contains(payload, secret) {
			t.Errorf("a credential was stored verbatim: %s", payload)
		}
	}
	// And the useful ones survive, or redaction has eaten the diagnostics.
	if !contains(payload, "curl/8") {
		t.Errorf("User-Agent was redacted along with the credentials: %s", payload)
	}
}

// TestANilRecorderIsAPassthrough, so installing the middleware with recording
// off is not a panic on every request.
func TestANilRecorderIsAPassthrough(t *testing.T) {
	called := false
	handler := provider.Middleware(nil)(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			called = true
			w.WriteHeader(http.StatusTeapot)
		}))

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))

	if !called {
		t.Error("the handler did not run with a nil recorder")
	}
	if response.Code != http.StatusTeapot {
		t.Errorf("status = %d, want the handler's 418", response.Code)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
