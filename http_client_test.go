package observe_test

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
)

// recording installs a recorder as the default for one test and takes it
// back down afterwards, because the default is a package-level singleton.
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

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store, q observe.Query) []observe.Entry {
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

type failingTransport struct{ err error }

func (f failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, f.err
}

func TestAnOutgoingRequestIsRecorded(t *testing.T) {
	recorder, store := recording(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}))
	t.Cleanup(upstream.Close)

	client := &http.Client{Transport: observe.WrapTransport(nil)}
	res, err := client.Get(upstream.URL + "/charges")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = res.Body.Close()

	found := drain(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindOutbound},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d outbound entries, want 1", len(found))
	}

	var payload struct {
		Method string `json:"method"`
		URL    string `json:"url"`
		Status int    `json:"status"`
	}
	if err := json.Unmarshal(found[0].Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Method != http.MethodGet || payload.Status != http.StatusAccepted {
		t.Errorf("payload = %+v", payload)
	}
	if !strings.HasSuffix(payload.URL, "/charges") {
		t.Errorf("URL = %q, want the path", payload.URL)
	}
	if found[0].Duration <= 0 {
		t.Error("no duration recorded, which is the reason to watch outgoing calls at all")
	}
}

// TestTheQueryStringIsNotRecorded.
//
// An API key in ?access_token= is extremely common, and a redactor only
// withholds what it recognises. Dropping the query entirely is the honest
// line, and the ?… is there so a reader knows there was one.
func TestTheQueryStringIsNotRecorded(t *testing.T) {
	recorder, store := recording(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	t.Cleanup(upstream.Close)

	client := &http.Client{Transport: observe.WrapTransport(nil)}
	res, err := client.Get(upstream.URL + "/v1/me?access_token=sk_live_not_a_real_key&x=1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_ = res.Body.Close()

	found := drain(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindOutbound},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(found))
	}

	// Checked against the whole entry, not only the payload: a summary built
	// from the raw URL would leak it just as effectively.
	blob, _ := json.Marshal(found[0])
	if strings.Contains(string(blob), "sk_live_not_a_real_key") {
		t.Errorf("the query string survived into the entry:\n%s", blob)
	}
	if !strings.Contains(found[0].Summary, "?…") {
		t.Errorf("summary does not show that a query was dropped: %q", found[0].Summary)
	}
}

func TestAFailedCallIsRecordedAsAnError(t *testing.T) {
	recorder, store := recording(t)

	client := &http.Client{
		Transport: observe.WrapTransport(failingTransport{err: errors.New("no route to host")}),
	}
	//nolint:bodyclose // there is no response to close.
	_, err := client.Get("https://example.invalid/x")
	if err == nil {
		t.Fatal("the error was swallowed")
	}

	found := drain(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindOutbound},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d entries, want 1 — a call that failed is the one "+
			"most worth recording", len(found))
	}
	if !strings.Contains(found[0].Err, "no route to host") {
		t.Errorf("Err = %q", found[0].Err)
	}
}

// TestItPassesThroughWithNoRecorder, because a transport that only worked
// while observe was enabled would be a trap: recording is off by default.
func TestItPassesThroughWithNoRecorder(t *testing.T) {
	observe.SetDefault(nil)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	t.Cleanup(upstream.Close)

	client := &http.Client{Transport: observe.WrapTransport(nil)}
	res, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want the upstream's own", res.StatusCode)
	}
}

func TestDumpRecordsWhatItIsGiven(t *testing.T) {
	recorder, store := recording(t)

	ctx := observe.WithRequestID(context.Background(), "req-9")
	observe.Dump(ctx, "why is this nil", map[string]any{"id": 7, "name": "ada"})

	found := drain(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindDump},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d dumps, want 1", len(found))
	}
	if found[0].Summary != "why is this nil" {
		t.Errorf("summary = %q", found[0].Summary)
	}
	// Correlation is most of the value: a dump beside the queries of the
	// same request is what a print statement cannot be.
	if found[0].RequestID != "req-9" {
		t.Errorf("RequestID = %q, want the request's", found[0].RequestID)
	}

	var payload struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(found[0].Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Values) != 1 || !strings.Contains(payload.Values[0], "ada") {
		t.Errorf("values = %v", payload.Values)
	}
}

// TestDumpSurvivesAValueThatWillNotEncode.
//
// A func, a channel or a cyclic structure fails json.Marshal, and dropping
// the entry would be the worst possible answer to "show me this thing".
func TestDumpSurvivesAValueThatWillNotEncode(t *testing.T) {
	recorder, store := recording(t)

	observe.Dump(context.Background(), "a channel", make(chan int))

	found := drain(t, recorder, store, observe.Query{
		Kinds: []observe.Kind{observe.KindDump},
	})
	if len(found) != 1 {
		t.Fatalf("recorded %d dumps, want 1", len(found))
	}
	var payload struct {
		Values []string `json:"values"`
	}
	if err := json.Unmarshal(found[0].Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if len(payload.Values) != 1 || payload.Values[0] == "" {
		t.Errorf("the value was dropped rather than rendered: %v", payload.Values)
	}
}
