package observeview_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observeview"
)

// page is a renderer that names itself, as inertia's response and
// res.Template do.
type page struct {
	name   string
	body   string
	err    error
	secret string
}

func (p page) ViewName() string { return p.name }

func (p page) Render(w io.Writer) error {
	_, _ = io.WriteString(w, p.body)
	return p.err
}

func watching(t *testing.T) (*observe.Recorder, observe.Store, app.App) {
	t.Helper()
	store := observememory.New()
	recorder := observe.New(store)
	observeview.Watch(recorder)
	t.Cleanup(func() {
		app.SetRenderObservers(nil)
		_ = recorder.Close(context.Background())
	})
	return recorder, store, app.Configure()
}

func render(t *testing.T, engine app.App, requestID string, r app.Renderer) {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/profile", nil)
	if requestID != "" {
		request = request.WithContext(observe.WithRequestID(request.Context(), requestID))
	}
	c := app.NewContext(engine, httptest.NewRecorder(), request)
	_ = c.Render(r)
}

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindView},
	})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

// The duration is the reason the panel exists: a page that is slow while its
// queries are fast is slow in the template, and a query entry cannot say so
// because a loop over an already-loaded relation issues no statements.
func TestARenderIsRecordedWithItsNameAndDuration(t *testing.T) {
	recorder, store, engine := watching(t)

	render(t, engine, "req-6", page{name: "Profile/Show", body: "hello"})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d views, want 1", len(found))
	}
	entry := found[0]
	if entry.RequestID != "req-6" {
		t.Errorf("RequestID = %q, want the request's", entry.RequestID)
	}
	if entry.Duration <= 0 {
		t.Error("no duration recorded, which is the reason to watch views at all")
	}

	var payload struct {
		View string `json:"view"`
	}
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.View != "Profile/Show" {
		t.Errorf("view = %q, want the component", payload.View)
	}
}

func TestAFailedRenderIsRecordedAsAnError(t *testing.T) {
	recorder, store, engine := watching(t)

	render(t, engine, "", page{
		name: "Profile/Show", body: "half a ",
		err: errors.New("the template does not exist"),
	})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d views, want 1", len(found))
	}
	if found[0].Level < slog.LevelError {
		t.Errorf("level = %v, want error", found[0].Level)
	}
	if found[0].Err == "" {
		t.Error("the renderer's error did not reach the entry")
	}
}

// No props and no output. An Inertia page's props are the response body, and
// the body is where a credential lives — the rule this package follows
// everywhere else.
func TestNeitherThePropsNorTheOutputIsRecorded(t *testing.T) {
	recorder, store, engine := watching(t)

	render(t, engine, "", page{
		name:   "Profile/Show",
		body:   "<script>window.props={token:'sk_live_not_a_real_key'}</script>",
		secret: "sk_live_not_a_real_key",
	})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d views, want 1", len(found))
	}
	blob, _ := json.Marshal(found[0])
	if contains(string(blob), "sk_live_not_a_real_key") {
		t.Errorf("the rendered output reached the entry:\n%s", blob)
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
