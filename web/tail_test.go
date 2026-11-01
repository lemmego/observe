package web_test

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/web"
)

// tailing returns an open dashboard over a real server.
//
// httptest.NewServer rather than NewRecorder, and that is not incidental: a
// ResponseRecorder buffers everything and returns only when the handler does,
// so a streaming handler would block forever and nothing about flushing could
// be observed at all. apptest learned the same thing.
func tailing(t *testing.T) (*httptest.Server, *observe.Recorder) {
	t.Helper()
	recorder := observe.New(memory.New())
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	dashboard := web.New(recorder, "")
	dashboard.UseAuth(func(next http.Handler) http.Handler { return next })

	server := httptest.NewServer(dashboard)
	t.Cleanup(server.Close)
	return server, recorder
}

// openTail connects and returns a reader positioned after the headers.
func openTail(t *testing.T, server *httptest.Server, query string) (*bufio.Reader, func()) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/tail"+query, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	response, err := http.DefaultClient.Do(request.WithContext(ctx))
	if err != nil {
		cancel()
		t.Fatal(err)
	}

	if response.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("the tail answered %d", response.StatusCode)
	}
	if got := response.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q", got)
	}
	// nginx buffers text/event-stream by default, so without this the stream
	// looks dead through a proxy and nowhere else — which is the worst place
	// for a bug to live.
	if got := response.Header.Get("X-Accel-Buffering"); got != "no" {
		t.Errorf("X-Accel-Buffering = %q, want no", got)
	}

	return bufio.NewReader(response.Body), func() {
		cancel()
		_ = response.Body.Close()
	}
}

// readFrame reads until a blank line and returns what it saw.
func readFrame(t *testing.T, reader *bufio.Reader, within time.Duration) string {
	t.Helper()
	type result struct {
		text string
		err  error
	}
	out := make(chan result, 1)
	go func() {
		var builder strings.Builder
		for {
			line, err := reader.ReadString('\n')
			if err != nil {
				out <- result{builder.String(), err}
				return
			}
			if line == "\n" {
				out <- result{builder.String(), nil}
				return
			}
			builder.WriteString(line)
		}
	}()

	select {
	case got := <-out:
		if got.err != nil && got.text == "" {
			t.Fatalf("reading a frame: %v", got.err)
		}
		return got.text
	case <-time.After(within):
		t.Fatalf("no frame arrived within %v", within)
		return ""
	}
}

// TestTheHeadersArriveBeforeTheFirstEntry.
//
// Flushed immediately, so "connected" is observable rather than
// indistinguishable from "hung" until something happens — which on a quiet
// application is a long time.
//
// The timing is the assertion and it is not decoration. Without the explicit
// flush this test still passes: net/http holds the headers until the handler
// writes enough or returns, and the handler eventually writes a heartbeat —
// so the only thing distinguishing a flushed stream from an unflushed one is
// how long the client waited. Measured against the heartbeat, which is left
// at its default here precisely so there is a gap to measure.
func TestTheHeadersArriveBeforeTheFirstEntry(t *testing.T) {
	server, _ := tailing(t)

	started := time.Now()
	_, close := openTail(t, server, "")
	close()

	if waited := time.Since(started); waited > 2*time.Second {
		t.Errorf("the headers took %v to arrive with nothing recorded; the "+
			"stream is not being flushed, so a client cannot tell "+
			"\"connected\" from \"hung\"", waited)
	}
}

func TestAnEntryRecordedAfterTheTailOpenedArrives(t *testing.T) {
	server, recorder := tailing(t)
	reader, close := openTail(t, server, "")
	defer close()

	// A beat, because the subscription is registered by the handler and the
	// client only knows the headers arrived.
	time.Sleep(100 * time.Millisecond)
	recorder.Record(context.Background(), observe.Entry{
		Kind: observe.KindQuery, Summary: "SELECT 1 FROM users",
	})

	frame := readFrame(t, reader, 3*time.Second)
	if !strings.Contains(frame, "event: entry") {
		t.Errorf("frame = %q, want an entry event", frame)
	}
	if !strings.Contains(frame, "SELECT 1 FROM users") {
		t.Errorf("frame = %q, want the entry", frame)
	}
	// An id per frame, so a reader can tell two identical entries apart —
	// and so the protocol is well-formed even though Last-Event-ID is
	// deliberately not honoured.
	if !strings.Contains(frame, "id: ") {
		t.Errorf("frame = %q, want an id line", frame)
	}
}

// Filtered server-side, so a tail of one kind does not ship every entry to
// the browser for it to throw away.
func TestTheTailHonoursTheFilter(t *testing.T) {
	server, recorder := tailing(t)
	reader, close := openTail(t, server, "?kind=exception")
	defer close()

	time.Sleep(100 * time.Millisecond)
	ctx := context.Background()
	recorder.Record(ctx, observe.Entry{Kind: observe.KindQuery, Summary: "not this one"})
	recorder.Record(ctx, observe.Entry{Kind: observe.KindException, Summary: "this one"})

	frame := readFrame(t, reader, 3*time.Second)
	if strings.Contains(frame, "not this one") {
		t.Errorf("the filter was ignored: %q", frame)
	}
	if !strings.Contains(frame, "this one") {
		t.Errorf("frame = %q", frame)
	}
}

// An application that records nothing for a minute is ordinary — it is three
// in the morning — and an idle intermediary closing the connection would make
// that look like a broken dashboard.
func TestAnIdleTailIsKeptAliveByAHeartbeat(t *testing.T) {
	web.SetTailHeartbeat(150 * time.Millisecond)
	t.Cleanup(func() { web.SetTailHeartbeat(20 * time.Second) })

	server, _ := tailing(t)
	reader, close := openTail(t, server, "")
	defer close()

	frame := readFrame(t, reader, 3*time.Second)
	// A comment frame: legal SSE, ignored by EventSource, and enough to keep
	// an intermediary from closing the connection.
	if !strings.HasPrefix(frame, ":") {
		t.Errorf("frame = %q, want a comment frame on an idle stream", frame)
	}
}

// The tail is a subscription rather than a poll, so a reader that disconnects
// must not leave a subscriber behind to be published to for the life of the
// recorder.
func TestDisconnectingEndsTheSubscription(t *testing.T) {
	server, recorder := tailing(t)
	_, close := openTail(t, server, "")

	time.Sleep(100 * time.Millisecond)
	if recorder.Subscribers() != 1 {
		t.Fatalf("Subscribers() = %d with one tail open", recorder.Subscribers())
	}

	close()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if recorder.Subscribers() == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the subscription outlived the connection")
}

// TestATailThatFallsBehindIsToldSoRatherThanGoingQuiet.
//
// Last-Event-ID is deliberately not honoured — there is no per-connection
// backlog, so resuming would be a promise the server cannot keep. What
// replaces it is saying so: a stream that simply ended would read as a
// working stream with nothing to report, which is the one reading a debugging
// tool must never produce.
func TestATailThatFallsBehindIsToldSoRatherThanGoingQuiet(t *testing.T) {
	server, recorder := tailing(t)

	// Connected but deliberately not read, so the handler's own subscriber
	// buffer fills behind it.
	request, err := http.NewRequest(http.MethodGet, server.URL+"/api/tail", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	response, err := http.DefaultClient.Do(request.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()

	time.Sleep(100 * time.Millisecond)

	// Comfortably past DefaultTailBuffer, and past whatever the socket
	// itself will absorb.
	for i := 0; i < 5000; i++ {
		recorder.Record(context.Background(), observe.Entry{
			Kind: observe.KindQuery, Summary: strings.Repeat("x", 200),
		})
	}

	reader := bufio.NewReader(response.Body)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("the stream ended without saying why: %v", err)
		}
		if strings.HasPrefix(line, "event: gap") {
			return
		}
	}
	t.Error("the tail never reported that it had fallen behind")
}
