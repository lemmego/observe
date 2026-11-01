package observenotify_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/lemmego/notify"
	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observenotify"
)

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

func drain(t *testing.T, recorder *observe.Recorder, store observe.Store) []observe.Entry {
	t.Helper()
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{
		Kinds: []observe.Kind{observe.KindNotification},
	})
	if err != nil {
		t.Fatalf("reading entries: %v", err)
	}
	return found
}

func TestADeliveredNotificationIsRecorded(t *testing.T) {
	recorder, store := recording(t)

	observenotify.Sink()(context.Background(), notify.Sent{
		Channel:      "mail",
		Notification: "*notifications.Matched",
		OwnerKind:    "user",
		OwnerID:      "7",
	})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d notifications, want 1", len(found))
	}
	if !strings.Contains(found[0].Summary, "Matched") ||
		!strings.Contains(found[0].Summary, "mail") {
		t.Errorf("summary says neither what was sent nor where: %q", found[0].Summary)
	}

	var payload struct {
		Notification  string `json:"notification"`
		Channel       string `json:"channel"`
		RecipientKind string `json:"recipient_kind"`
		RecipientID   string `json:"recipient_id"`
		Correlation   string `json:"correlation"`
	}
	if err := json.Unmarshal(found[0].Data, &payload); err != nil {
		t.Fatalf("payload: %v", err)
	}
	if payload.RecipientKind != "user" || payload.RecipientID != "7" {
		t.Errorf("payload does not say who it went to: %+v", payload)
	}
	// Sent with no request behind it, which is what a background job looks
	// like. Said rather than left blank, so a reader stops hunting for a
	// request that was never recorded.
	if payload.Correlation != "unavailable" {
		t.Errorf("correlation = %q, want it stated", payload.Correlation)
	}
}

func TestAFailedNotificationIsRecordedAsAnError(t *testing.T) {
	recorder, store := recording(t)

	observenotify.Sink()(context.Background(), notify.Failed{
		Channel:      "mail",
		Notification: "*notifications.Matched",
		OwnerKind:    "user",
		OwnerID:      "7",
		Err:          errors.New("smtp: connection refused"),
	})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d notifications, want 1", len(found))
	}
	if found[0].Level < slog.LevelError {
		t.Errorf("a failed delivery is recorded at %v, want error level", found[0].Level)
	}
	if !strings.Contains(found[0].Err, "connection refused") {
		t.Errorf("Err = %q, want the cause", found[0].Err)
	}
}

// TestSendingIsNotRecorded.
//
// notify emits Sending before Sent, so recording both would show every
// notification twice — once for being attempted and once for the outcome —
// and the panel would read as double the traffic.
func TestSendingIsNotRecorded(t *testing.T) {
	recorder, store := recording(t)

	sink := observenotify.Sink()
	sink(context.Background(), notify.Sending{Channel: "mail", Notification: "*notifications.Matched"})
	sink(context.Background(), notify.Sent{Channel: "mail", Notification: "*notifications.Matched"})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d entries for one delivery, want 1", len(found))
	}
}

// TestNoRecipientContactDetailsAreRecorded.
//
// The recipient is notify's own kind/id pair and nothing else. An email
// address or a phone number is a routing detail that belongs to the channel,
// and recording it here would put contact details in a debugging store for
// every notification the application ever sends.
func TestNoRecipientContactDetailsAreRecorded(t *testing.T) {
	recorder, store := recording(t)

	observenotify.Sink()(context.Background(), notify.Sent{
		Channel:      "mail",
		Notification: "*notifications.Matched",
		OwnerKind:    "user",
		OwnerID:      "7",
	})

	found := drain(t, recorder, store)
	if len(found) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(found))
	}
	blob, _ := json.Marshal(found[0])
	if strings.Contains(string(blob), "@") {
		t.Errorf("something address-shaped reached the entry:\n%s", blob)
	}
}

func TestItDoesNothingWithNoRecorder(t *testing.T) {
	observe.SetDefault(nil)
	// The assertion is that this does not panic and does not reach for a
	// store that is not there.
	observenotify.Sink()(context.Background(), notify.Sent{Channel: "mail", Notification: "x"})
}
