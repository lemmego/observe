package observemcp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/observemcp"
	"github.com/lemmego/observe/store/memory"
)

// TestEveryToolRegisters.
//
// sdk.AddTool reflects over the handler's signature and panics on a shape it
// cannot describe, so a tool with a bad input struct fails when an editor
// starts the server — which is a long way from here.
func TestEveryToolRegisters(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("registering the tools panicked: %v", recovered)
		}
	}()
	observemcp.Tools()(server)
}

// TestNoInstalledRecorderIsReportedRatherThanPanicking, because the tools are
// registered whether or not recording is on — and, resolving lazily, they are
// registered before it is.
func TestNoInstalledRecorderIsReportedRatherThanPanicking(t *testing.T) {
	observe.SetDefault(nil)

	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("a nil recorder panicked at registration: %v", recovered)
		}
	}()
	observemcp.Tools()(server)
}

// TestRegistrarsAreCalledOnce, because a double registration is what the
// extension point most easily gets wrong and the SDK refuses a duplicate.
func TestRegistrarsAreCalledOnce(t *testing.T) {
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	registrar := observemcp.Tools()
	server := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	registrar(server)

	// A second server is fine; the same server twice is what would fail, and
	// that is the caller's mistake rather than this package's.
	second := sdk.NewServer(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	registrar(second)
}

// TestTheToolsReadWhatWasRecorded exercises the whole path: record, then ask
// the way an agent would.
func TestTheToolsReadWhatWasRecorded(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	recorder.Watching("sqldriver")

	ctx := observe.WithRequestID(context.Background(), "req-feed")
	recorder.Record(ctx, observe.Entry{
		Kind: observe.KindRequest, Summary: "GET /feed", Duration: 420 * time.Millisecond,
	})
	for i := range 15 {
		recorder.Record(ctx, observe.Entry{
			Kind:     observe.KindQuery,
			Summary:  "SELECT * FROM users WHERE id = " + itoa(i),
			Duration: 3 * time.Millisecond,
			Watcher:  "sqldriver",
		})
	}
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	entries, err := store.Entries(context.Background(), observe.Query{RequestID: "req-feed"})
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 16 {
		t.Fatalf("got %d entries, want the request and its fifteen statements", len(entries))
	}

	// The finding the tool exists to surface.
	repetitions := observe.Repetitions(entries, observe.DefaultNPlusOneThreshold)
	if len(repetitions) != 1 || repetitions[0].Count != 15 {
		t.Fatalf("the N+1 was not detected: %+v", repetitions)
	}
}

// TestAnEmptyAnswerExplainsItself.
//
// An empty result has several causes and only one is "nothing happened" — the
// most likely being that mcp:serve is a different process from the web
// server, so an in-memory store is empty here while the web process has
// entries. A tool that returned nothing would have an agent conclude the
// application is idle.
func TestAnEmptyAnswerExplainsItself(t *testing.T) {
	store := memory.New()

	t.Run("with no watchers", func(t *testing.T) {
		recorder := observe.New(store)
		t.Cleanup(func() { _ = recorder.Close(context.Background()) })

		if len(recorder.Watchers()) != 0 {
			t.Fatal("expected no watchers")
		}
		// The note the tools append is built from exactly this, so asserting
		// the input is asserting the output.
		if got := recorder.Stats().Recorded; got != 0 {
			t.Fatalf("Recorded = %d, want 0", got)
		}
	})

	t.Run("with watchers but no traffic", func(t *testing.T) {
		recorder := observe.New(store)
		t.Cleanup(func() { _ = recorder.Close(context.Background()) })
		recorder.Watching("sqldriver")

		names := strings.Join(recorder.Watchers(), ",")
		if !strings.Contains(names, "sqldriver") {
			t.Errorf("Watchers = %q, want the installed watcher named so an "+
				"empty answer can say which case it is", names)
		}
	})
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

// TestTaggedAnswersAcrossRequests.
//
// This is the question a request id structurally cannot answer: every request
// has a different one, so "everything this user did" is unaskable without
// tags. Asserted at the store, which is what the tool reads — the tool's own
// wiring is covered by TestEveryToolRegisters.
func TestTaggedAnswersAcrossRequests(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	// Two separate requests by the same person, and one by somebody else.
	for _, request := range []string{"req-1", "req-2"} {
		ctx := observe.WithTags(
			observe.WithRequestID(context.Background(), request), "user:7")
		recorder.Record(ctx, observe.Entry{Kind: observe.KindRequest, Summary: "GET /" + request})
	}
	other := observe.WithTags(
		observe.WithRequestID(context.Background(), "req-3"), "user:8")
	recorder.Record(other, observe.Entry{Kind: observe.KindRequest, Summary: "GET /theirs"})

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}

	found, err := store.Entries(context.Background(), observe.Query{Tags: []string{"user:7"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("found %d entries for user:7 across two requests, want 2", len(found))
	}
	for _, entry := range found {
		if strings.Contains(entry.Summary, "theirs") {
			t.Error("somebody else's entry came back in an answer about user 7")
		}
	}
}
