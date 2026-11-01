package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/lemmego/observe"
)

// tailHeartbeat is how often a comment frame is sent on an idle stream.
//
// Below any proxy idle timeout worth worrying about. An application that
// records nothing for a minute is ordinary — it is three in the morning — and
// an idle intermediary closing the connection would make that look like a
// broken dashboard.
var tailHeartbeat = 20 * time.Second

// SetTailHeartbeat changes the interval, for a test that cannot wait twenty
// seconds to prove an idle stream stays open.
//
// Exported rather than a field on Server because the value is a property of
// the protocol rather than of one dashboard, and a test setting it on one
// server while another streams would be the kind of shared state this package
// already had to fix in the recorder.
func SetTailHeartbeat(d time.Duration) {
	if d > 0 {
		tailHeartbeat = d
	}
}

// handleTail streams entries as they are recorded.
//
// Four things this does that tasker's dashboard stream does not, each of them
// a bug in that one:
//
//   - It flushes through http.NewResponseController rather than asserting
//     http.Flusher. A type assertion traverses nothing, so a wrapped writer
//     — which every middleware in this framework installs — makes the
//     assertion false and answers 500 "streaming not supported". That is
//     exactly what tasker's own event stream did behind RequestLogger.
//   - It sets X-Accel-Buffering: no, or nginx buffers text/event-stream and
//     the stream looks dead through a proxy and nowhere else.
//   - It flushes the headers immediately, so "connected" is observable
//     rather than indistinguishable from "hung" until the first entry.
//   - It is a subscription rather than a ticker re-reading the store. The
//     poll it replaces was a 5-second setInterval, so the dashboard was
//     always up to five seconds stale and asked the database for the same
//     rows over and over to say so.
//
// Last-Event-ID is deliberately not honoured. There is no per-connection
// backlog, so resuming would be a promise the server cannot keep — and a
// client that silently missed entries is worse than one that knows it did.
// The stream says so instead: when a subscriber falls behind, the recorder
// closes it and this sends a `gap` event before ending, so the page can say
// what happened rather than go quiet.
func (s *Server) handleTail(w http.ResponseWriter, r *http.Request) {
	if s.recorder == nil {
		writeError(w, http.StatusServiceUnavailable, "observe: recording is off")
		return
	}

	// Filtered server-side, so a tail of one kind does not ship every entry
	// to the browser for it to throw away.
	query, err := decodeQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	controller := http.NewResponseController(w)
	//nolint:errcheck // a client that hung up is not an error worth reporting.
	_ = controller.Flush()

	entries, stop := s.recorder.Subscribe(0)
	defer stop()

	heartbeat := time.NewTicker(tailHeartbeat)
	defer heartbeat.Stop()

	// The server's WriteTimeout is 30s and a tail outlives that by design, so
	// the deadline is re-armed per frame rather than cleared outright. A
	// cleared deadline means a connection that can hang forever; a re-armed
	// one means a frame that cannot.
	rearm := func() {
		//nolint:errcheck // a writer that cannot take a deadline still works.
		_ = controller.SetWriteDeadline(time.Now().Add(tailHeartbeat * 2))
	}
	rearm()

	for {
		select {
		case <-r.Context().Done():
			return

		case entry, open := <-entries:
			if !open {
				// Either the recorder shut down or this subscriber fell
				// behind and was closed. Said out loud, because a stream
				// that ends silently reads as a working stream with nothing
				// to say.
				writeEvent(w, "gap", `{"reason":"the tail fell behind or the recorder stopped"}`)
				//nolint:errcheck // the stream is ending either way.
				_ = controller.Flush()
				return
			}
			if !matchesTail(entry, query) {
				continue
			}
			encoded, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			rearm()
			if !writeEvent(w, "entry", string(encoded), entry.ID) {
				return
			}
			//nolint:errcheck // a client that hung up ends the stream below.
			_ = controller.Flush()

		case <-heartbeat.C:
			rearm()
			// A comment frame: legal SSE, ignored by EventSource, and enough
			// to keep an intermediary from closing an idle connection.
			if _, err := fmt.Fprint(w, ": still here\n\n"); err != nil {
				return
			}
			//nolint:errcheck // as above.
			_ = controller.Flush()
		}
	}
}

// writeEvent sends one frame and reports whether the client is still there.
func writeEvent(w http.ResponseWriter, event, data string, id ...string) bool {
	if len(id) > 0 && id[0] != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id[0]); err != nil {
			return false
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return false
	}
	return true
}

// matchesTail applies the filters a tail can honour in this process.
//
// Kind, level, request and tags are properties of the entry, so they are
// decided here. Since, Until and the cursor are not: they select a window of
// history, and a tail is the opposite of a window — it is whatever happens
// next. They are ignored rather than refused, because the dashboard sends the
// same query string to both endpoints and refusing would mean two.
func matchesTail(entry observe.Entry, q observe.Query) bool {
	if len(q.Kinds) > 0 {
		found := false
		for _, kind := range q.Kinds {
			if kind == entry.Kind {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q.MinLevel != nil && entry.Level < *q.MinLevel {
		return false
	}
	if q.RequestID != "" && entry.RequestID != q.RequestID {
		return false
	}
	if !observe.HasTags(entry, q.Tags) {
		return false
	}
	if q.Contains != "" && !containsFold(entry, q.Contains) {
		return false
	}
	return true
}
