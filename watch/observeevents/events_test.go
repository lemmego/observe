package observeevents_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
	observememory "github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observeevents"
)

type launch struct {
	ID     uint64
	Secret string
}

func watching(t *testing.T, events ...string) (*observe.Recorder, observe.Store, app.App) {
	t.Helper()
	store := observememory.New()
	recorder := observe.New(store)
	engine := app.Configure()
	observeevents.Watch(engine, recorder, events...)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	return recorder, store, engine
}

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindEvent},
	})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

// app.Dispatch leaves nothing behind, so whether the dispatch happened at all
// is not recoverable from anywhere — and a feature whose cross-cutting
// reaction never fires looks identical to one whose listener is broken.
func TestADispatchedEventIsRecordedAgainstTheRequest(t *testing.T) {
	recorder, store, engine := watching(t, "launch.published")

	engine.DispatchContext(
		observe.WithRequestID(context.Background(), "req-8"),
		"launch.published", &launch{ID: 3})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d events, want 1", len(found))
	}
	if found[0].RequestID != "req-8" {
		t.Errorf("RequestID = %q, want the dispatcher's", found[0].RequestID)
	}

	var payload struct {
		Event       string `json:"event"`
		PayloadType string `json:"payload_type"`
	}
	if err := json.Unmarshal(found[0].Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.Event != "launch.published" {
		t.Errorf("event = %q", payload.Event)
	}
	if payload.PayloadType != "*observeevents_test.launch" {
		t.Errorf("payload_type = %q, want the Go type", payload.PayloadType)
	}
}

// Only what it was told to watch. The emitter's only registration takes a
// name, so there is no "every event" to offer — and covering some events
// silently would be worse than naming them.
func TestAnEventItWasNotToldAboutIsNotRecorded(t *testing.T) {
	recorder, store, engine := watching(t, "launch.published")

	engine.Dispatch("comment.created", &launch{ID: 3})

	if found := drain(t, recorder, store); len(found) != 0 {
		t.Errorf("recorded %d events for a name nobody asked for", len(found))
	}
}

// Not the payload. An application's event payloads are its own domain
// objects, and a *models.User is a plausible one.
func TestThePayloadIsNotRecorded(t *testing.T) {
	recorder, store, engine := watching(t, "launch.published")

	engine.Dispatch("launch.published", &launch{ID: 3, Secret: "sk_live_not_a_real_key"})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d events, want 1", len(found))
	}
	blob, _ := json.Marshal(found[0])
	if strings.Contains(string(blob), "sk_live_not_a_real_key") {
		t.Errorf("the payload reached the entry:\n%s", blob)
	}
}

// An event dispatched at boot or shutdown has no request behind it, which is
// ordinary and must not stop it being recorded.
func TestAnEventWithNoRequestIsStillRecorded(t *testing.T) {
	recorder, store, engine := watching(t, app.ServerStarted)

	engine.Dispatch(app.ServerStarted)

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d events, want 1", len(found))
	}
	if found[0].RequestID != "" {
		t.Errorf("RequestID = %q for an event dispatched outside a request", found[0].RequestID)
	}
}
