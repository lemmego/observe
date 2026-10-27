package observeslog_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/watch/observeslog"
)

func newLogger(t *testing.T) (*slog.Logger, observe.Store, *observe.Recorder) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	return slog.New(observeslog.Handler()), store, recorder
}

// TestALogLineLandsUnderItsRequest, which is what turns a request view from a
// list of queries into an account of what happened.
func TestALogLineLandsUnderItsRequest(t *testing.T) {
	logger, store, recorder := newLogger(t)

	ctx := observe.WithRequestID(context.Background(), "req-1")
	logger.WarnContext(ctx, "the upstream was slow", "took_ms", 812)

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: "req-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries under the request, want 1", len(entries))
	}

	entry := entries[0]
	if entry.Kind != observe.KindLog {
		t.Errorf("Kind = %q, want log", entry.Kind)
	}
	if entry.Level != slog.LevelWarn {
		t.Errorf("Level = %v, want warn", entry.Level)
	}
	if entry.Summary != "the upstream was slow" {
		t.Errorf("Summary = %q", entry.Summary)
	}
	if !strings.Contains(string(entry.Data), "812") {
		t.Errorf("the attributes were not recorded: %s", entry.Data)
	}
}

// TestALineWithoutAContextIsRecordedUncorrelated rather than dropped.
//
// slog.Info carries no context where slog.InfoContext does, and plenty of
// library code uses the first. Those lines are still worth having.
func TestALineWithoutAContextIsRecordedUncorrelated(t *testing.T) {
	logger, store, recorder := newLogger(t)

	logger.Info("a line from somewhere with no context")
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want the line recorded anyway", len(entries))
	}
	if entries[0].RequestID != "" {
		t.Errorf("RequestID = %q, want empty", entries[0].RequestID)
	}
}

// TestObservesOwnComplaintsAreNotRecorded.
//
// A saturated queue logs that it is dropping entries. Recording that line is
// one more entry for a queue that is already full.
func TestObservesOwnComplaintsAreNotRecorded(t *testing.T) {
	logger, store, recorder := newLogger(t)

	logger.Warn("observe: the entry queue is full, so entries are being dropped")
	logger.Warn("a line that is not observe's")

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want only the line that is not observe's", len(entries))
	}
	if strings.HasPrefix(entries[0].Summary, "observe:") {
		t.Error("observe's own complaint was recorded")
	}
}

// TestNothingIsRecordedUnderWithoutRecording, the context half of the
// recursion guard.
func TestNothingIsRecordedUnderWithoutRecording(t *testing.T) {
	logger, _, recorder := newLogger(t)

	logger.InfoContext(observe.WithoutRecording(context.Background()), "a store's own write")

	if got := recorder.Stats().Recorded; got != 0 {
		t.Errorf("%d entries were recorded under WithoutRecording", got)
	}
}

// TestAnErrorAttributeIsPromoted, so "show me what went wrong" finds it
// without reading every entry's payload.
func TestAnErrorAttributeIsPromoted(t *testing.T) {
	logger, store, recorder := newLogger(t)

	logger.Error("writing the avatar failed", "error", "disk full")
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
	if entries[0].Err != "disk full" {
		t.Errorf("Err = %q, want the error attribute promoted", entries[0].Err)
	}
	if !entries[0].Failed() {
		t.Error("the entry does not report as failed")
	}
}

// TestSecretAttributesAreRedacted. A log line is where a token gets written
// down by accident.
func TestSecretAttributesAreRedacted(t *testing.T) {
	logger, store, recorder := newLogger(t)

	logger.Info("signing in", "user", "ada", "password", "hunter2",
		"database_url", "postgres://u:sekrit@host:5432/db")

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	payload := string(entries[0].Data)

	for _, secret := range []string{"hunter2", "sekrit"} {
		if strings.Contains(payload, secret) {
			t.Errorf("a secret was stored verbatim: %s", payload)
		}
	}
	if !strings.Contains(payload, "ada") {
		t.Errorf("a harmless attribute was redacted too: %s", payload)
	}
	// The connection string survives minus its password, which is the point
	// of rewriting a credential inside a value rather than dropping the key.
	if !strings.Contains(payload, "host:5432") {
		t.Errorf("the whole DSN was dropped rather than its password: %s", payload)
	}
}

// TestGroupsAndAttrsAreCarried, because a logger derived with attrs is a
// different handler object and the derived one is what actually logs.
func TestGroupsAndAttrsAreCarried(t *testing.T) {
	logger, store, recorder := newLogger(t)

	derived := logger.With("service", "feed").WithGroup("upstream")
	derived.Info("called", "status", 503)

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}

	var fields map[string]any
	if err := json.Unmarshal(entries[0].Data, &fields); err != nil {
		t.Fatalf("decoding: %v (%s)", err, entries[0].Data)
	}
	if fields["service"] != "feed" {
		t.Errorf("an attribute from With was lost: %v", fields)
	}
	// Qualified by its group, so two "status" attrs in different groups stay
	// apart.
	if _, ok := fields["upstream.status"]; !ok {
		t.Errorf("a grouped attribute was not qualified: %v", fields)
	}
}

// TestDerivingTwiceDoesNotShareBackingArrays, the classic append-aliasing bug
// — two loggers from one parent silently overwriting each other's attrs.
func TestDerivingTwiceDoesNotShareBackingArrays(t *testing.T) {
	logger, store, recorder := newLogger(t)

	parent := logger.With("shared", "yes")
	first := parent.With("which", "first")
	second := parent.With("which", "second")

	first.Info("one")
	second.Info("two")

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	entries, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	seen := map[string]bool{}
	for _, entry := range entries {
		var fields map[string]any
		if err := json.Unmarshal(entry.Data, &fields); err != nil {
			t.Fatal(err)
		}
		which, _ := fields["which"].(string)
		seen[which] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Errorf("the two derived loggers shared a backing array: %v", seen)
	}
}
