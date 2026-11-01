package observeauthz_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lemmego/api/app"
	"github.com/lemmego/auth"
	"github.com/lemmego/authz"
	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observeauthz"
)

type user struct {
	ID    string
	Email string
}

func (u *user) GetID() string       { return u.ID }
func (u *user) GetUsername() string { return u.Email }
func (u *user) GetPassword() string { return "" }

type launch struct {
	ID     uint64
	Secret string
}

// harness builds the smallest thing that can reach authz's own decide: a
// configured engine holding the registry, and a context over a recorder.
//
// Through authz.Can rather than by calling decide, which is unexported — and
// that is the right way round anyway: what is under test is that a decision
// made the way an application makes one is recorded.
type harness struct {
	registry *authz.Registry
	recorder *observe.Recorder
	store    observe.Store
	engine   app.App
}

func watching(t *testing.T, policies func(*authz.Registry)) *harness {
	t.Helper()
	store := observememory.New()
	recorder := observe.New(store)
	registry := authz.NewRegistry()
	if policies != nil {
		policies(registry)
	}
	observeauthz.Watch(registry, recorder)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	engine := app.Configure()
	engine.AddService(registry)
	return &harness{registry: registry, recorder: recorder, store: store, engine: engine}
}

// ask makes one decision as a handler would, optionally signed in.
func (h *harness) ask(ability string, resource *launch, as *user) {
	request := httptest.NewRequest(http.MethodGet, "/launch", nil)
	c := app.NewContext(h.engine, httptest.NewRecorder(), request)
	if as != nil {
		c.Set(auth.UserKey, as)
	}
	_ = authz.Can(c, ability, resource)
}

func (h *harness) drain(t *testing.T) []observe.Entry {
	t.Helper()
	if err := h.recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := h.store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindGate},
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

func ownership(r *authz.Registry) {
	authz.Register(r, "launch.update",
		func(_ app.Context, u *user, l *launch) authz.Decision {
			if u.ID == "1" {
				return authz.Allow()
			}
			return authz.Deny("not your launch")
		})
}

// Both outcomes, because a panel showing only refusals cannot show the allow
// nobody intended — and the two failures are symmetrical.
func TestBothOutcomesAreRecorded(t *testing.T) {
	h := watching(t, ownership)

	h.ask("launch.update", &launch{ID: 1}, &user{ID: "1"})
	h.ask("launch.update", &launch{ID: 1}, &user{ID: "2"})

	found := h.drain(t)
	if len(found) != 2 {
		t.Fatalf("recorded %d decisions, want 2", len(found))
	}

	var allowed, denied bool
	for _, entry := range found {
		if payloadOf(t, entry)["allowed"] == true {
			allowed = true
			if entry.Level != slog.LevelInfo {
				t.Errorf("an allow was recorded at %v", entry.Level)
			}
		} else {
			denied = true
			// Warn rather than error: a refusal is the system working, and
			// error level would make the errors view useless on any
			// application that refuses anything.
			if entry.Level != slog.LevelWarn {
				t.Errorf("a denial was recorded at %v, want warn", entry.Level)
			}
		}
	}
	if !allowed || !denied {
		t.Errorf("allowed=%v denied=%v; both outcomes must be recorded", allowed, denied)
	}
}

// The denials authz reaches without consulting a rule are the ones worth
// having. It fails closed, so a typo in an ability name is an
// ordinary-looking 403, and the reason is what stops it looking ordinary.
func TestADenialReachedWithoutARuleCarriesItsReason(t *testing.T) {
	// launch.update is deliberately not registered.
	h := watching(t, nil)

	h.ask("launch.update", &launch{ID: 1}, &user{ID: "1"})

	found := h.drain(t)
	if len(found) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(found))
	}
	reason, _ := payloadOf(t, found[0])["reason"].(string)
	if !strings.Contains(reason, "no rule is registered") {
		t.Errorf("reason = %q, want authz's own prose", reason)
	}
}

// An anonymous request is distinguished from a subject this watcher could not
// name, which otherwise look the same in a list of blank subjects.
func TestAnAnonymousRequestIsSaidToBeAnonymous(t *testing.T) {
	h := watching(t, ownership)

	h.ask("launch.update", &launch{ID: 1}, nil)

	found := h.drain(t)
	if len(found) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(found))
	}
	payload := payloadOf(t, found[0])
	if payload["anonymous"] != true {
		t.Errorf("anonymous = %v for a request with nobody signed in", payload["anonymous"])
	}
	if _, named := payload["subject_id"]; named {
		t.Errorf("subject_id = %v for an anonymous request", payload["subject_id"])
	}
}

// Neither value is recorded. A user struct holds an email address and a
// password hash; a resource holds whatever the application put in it.
func TestNeitherTheSubjectNorTheResourceIsRecorded(t *testing.T) {
	h := watching(t, ownership)

	h.ask("launch.update",
		&launch{ID: 1, Secret: "sk_live_not_a_real_key"},
		&user{ID: "7", Email: "ada@example.com"})

	found := h.drain(t)
	if len(found) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(found))
	}

	// Against the whole entry, not the payload: a summary built from either
	// value would leak it just as effectively.
	blob, _ := json.Marshal(found[0])
	for _, leaked := range []string{"ada@example.com", "sk_live_not_a_real_key"} {
		if strings.Contains(string(blob), leaked) {
			t.Errorf("%q reached the entry:\n%s", leaked, blob)
		}
	}

	// But who was refused what is still legible, which is why the id is
	// pulled off the subject rather than the subject being dropped whole.
	payload := payloadOf(t, found[0])
	if payload["subject_id"] != "7" {
		t.Errorf("subject_id = %v, want the user's id", payload["subject_id"])
	}
	if payload["resource_type"] != "*observeauthz_test.launch" {
		t.Errorf("resource_type = %v", payload["resource_type"])
	}
}

// The request's context, so a gate decision reads beside the queries of the
// same request rather than as a line on its own.
func TestADecisionCarriesTheRequestsID(t *testing.T) {
	h := watching(t, ownership)

	request := httptest.NewRequest(http.MethodGet, "/launch", nil)
	request = request.WithContext(observe.WithRequestID(request.Context(), "req-5"))
	c := app.NewContext(h.engine, httptest.NewRecorder(), request)
	c.Set(auth.UserKey, &user{ID: "1"})
	_ = authz.Can(c, "launch.update", &launch{ID: 1})

	found := h.drain(t)
	if len(found) != 1 {
		t.Fatalf("recorded %d decisions, want 1", len(found))
	}
	if found[0].RequestID != "req-5" {
		t.Errorf("RequestID = %q, want the request's", found[0].RequestID)
	}
}
