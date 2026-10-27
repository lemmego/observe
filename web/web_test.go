package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/web"
)

func newServer(t *testing.T) (*web.Server, observe.Store) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	recorder.Watching("sqldriver")
	return web.New(recorder, "/observe"), store
}

// TestTheDashboardIsClosedUntilAuthenticationIsConfigured.
//
// Nil is not "no authentication", it is "no access". These entries hold
// request headers, bound query parameters and every statement the
// application ran, so the failure mode of the opposite default is a
// credential dump behind a nice table.
func TestTheDashboardIsClosedUntilAuthenticationIsConfigured(t *testing.T) {
	server, _ := newServer(t)

	for _, path := range []string{"/observe/", "/observe/api/entries", "/observe/api/stats"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d with no auth configured, want 401", path, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "<table") {
			t.Errorf("%s served the dashboard with no auth configured", path)
		}
	}
}

// TestUseDoesNotOpenTheDashboard, because middleware that is not
// authentication must not be mistakable for it.
func TestUseDoesNotOpenTheDashboard(t *testing.T) {
	server, _ := newServer(t)
	server.Use(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("Use opened the dashboard (answered %d); only UseAuth may", recorder.Code)
	}
}

func TestUseAuthOpensIt(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("the dashboard answered %d with auth configured, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<title>observe</title>") {
		t.Error("the dashboard did not render")
	}
}

// TestAnAbsurdLimitIsClamped. A limit arriving from a query string is the
// normal case, and an unbounded one is a way to ask for the whole table.
func TestAnAbsurdLimitIsClamped(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	entries := make([]observe.Entry, 0, observe.MaxPageSize+50)
	for i := range observe.MaxPageSize + 50 {
		entries = append(entries, observe.Entry{
			ID:   "entry-" + itoa(i),
			Kind: observe.KindQuery,
		})
	}
	if err := store.Record(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/observe/api/entries?limit=10000000", nil))

	var body struct {
		Data  []observe.Entry `json:"data"`
		Limit int             `json:"limit"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if len(body.Data) > observe.MaxPageSize {
		t.Errorf("got %d entries for a limit of ten million; it was not clamped",
			len(body.Data))
	}
	if body.Limit > observe.MaxPageSize {
		t.Errorf("the reported limit is %d, want it clamped", body.Limit)
	}
}

// TestABadFilterIsRefusedRatherThanIgnored.
//
// Silently ignoring an unparsable filter shows the unfiltered table, which
// reads as "there is nothing matching" — the one answer a debugger must not
// be given wrongly.
func TestABadFilterIsRefusedRatherThanIgnored(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	for _, query := range []string{"limit=abc", "level=lourd", "since=yesterday"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/observe/api/entries?"+query, nil))

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("?%s answered %d, want 400 — an ignored filter shows the "+
				"unfiltered table and reads as 'nothing matched'", query, recorder.Code)
		}
	}
}

// TestARequestViewCarriesItsRepetitions, which is the finding somebody opens
// a request to look for.
func TestARequestViewCarriesItsRepetitions(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	entries := []observe.Entry{{
		ID: "req", Kind: observe.KindRequest, RequestID: "r1", Summary: "GET /feed",
	}}
	for i := range 20 {
		entries = append(entries, observe.Entry{
			ID:        "q-" + itoa(i),
			Kind:      observe.KindQuery,
			RequestID: "r1",
			Summary:   "SELECT * FROM users WHERE id = " + itoa(i),
		})
	}
	if err := store.Record(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/requests/r1", nil))

	var body struct {
		Data        []observe.Entry      `json:"data"`
		Repetitions []observe.Repetition `json:"repetitions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if len(body.Data) != 21 {
		t.Errorf("got %d entries under the request, want 21", len(body.Data))
	}
	if len(body.Repetitions) != 1 || body.Repetitions[0].Count != 20 {
		t.Errorf("the N+1 was not reported: %+v", body.Repetitions)
	}
}

// TestStatsNameTheWatchers, so an empty table can be told from an unwatched
// one.
func TestStatsNameTheWatchers(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/stats", nil))

	var body struct {
		Watchers []string `json:"watchers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Watchers) == 0 {
		t.Error("stats named no watchers, so an empty table cannot be told " +
			"from one nothing is watching")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
