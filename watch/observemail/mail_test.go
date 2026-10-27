package observemail_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/lemmego/mail"
	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observemail"
)

type fakeTransport struct {
	sent int
	err  error
}

func (f *fakeTransport) Send(mail.Context, *mail.Message) error {
	f.sent++
	return f.err
}

// TestItDecoratesRatherThanReplaces, which is the distinction that matters:
// mailtest's recorder substitutes for the transport and delivers nothing, so
// it cannot be used to watch a real one.
func TestItDecoratesRatherThanReplaces(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	real := &fakeTransport{}
	watched := observemail.Watch(real, recorder)

	err := watched.Send(context.Background(), &mail.Message{
		Subject: "Confirm your address",
		From:    mail.Address{Email: "hello@example.com"},
		To:      []mail.Address{{Email: "ada@example.com"}},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if real.sent != 1 {
		t.Fatalf("the real transport sent %d messages, want 1 — the watcher "+
			"replaced it rather than wrapping it", real.sent)
	}
}

// TestTheBodyIsNotRecorded.
//
// A rendered body is where a password-reset link lives, and a link is a
// credential: anybody who could read this table could use it.
func TestTheBodyIsNotRecorded(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)

	watched := observemail.Watch(&fakeTransport{}, recorder)
	ctx := observe.WithRequestID(context.Background(), "req-1")

	if err := watched.Send(ctx, &mail.Message{
		Subject: "Reset your password",
		From:    mail.Address{Email: "hello@example.com"},
		To:      []mail.Address{{Email: "ada@example.com"}},
		HTML:    `<a href="https://example.com/reset?token=sekrit">Reset</a>`,
		Text:    "https://example.com/reset?token=sekrit",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	payload := string(entries[0].Data)
	if strings.Contains(payload, "sekrit") {
		t.Errorf("a reset token was recorded: %s", payload)
	}
	// The envelope is what answers "was it sent, and to whom".
	for _, want := range []string{"Reset your password", "ada@example.com"} {
		if !strings.Contains(payload, want) {
			t.Errorf("the envelope is missing %q: %s", want, payload)
		}
	}
}

// TestAFailedSendIsRecordedAndReturned, because a swallowed error here would
// be worse than no recording at all.
func TestAFailedSendIsRecordedAndReturned(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)

	boom := errors.New("no route to host")
	watched := observemail.Watch(&fakeTransport{err: boom}, recorder)

	err := watched.Send(context.Background(), &mail.Message{Subject: "hi"})
	if !errors.Is(err, boom) {
		t.Fatalf("Send returned %v, want the transport's error", err)
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !entries[0].Failed() {
		t.Errorf("the failure was not recorded: %+v", entries)
	}
}

// TestANilRecorderReturnsTheTransportUntouched, so wiring it with recording
// off costs nothing.
func TestANilRecorderReturnsTheTransportUntouched(t *testing.T) {
	real := &fakeTransport{}
	if got := observemail.Watch(real, nil); got != mail.Transport(real) {
		t.Error("a nil recorder produced a wrapper rather than the transport itself")
	}
}
