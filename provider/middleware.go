package provider

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe"
)

// WatcherHTTP is the watcher name request entries carry.
const WatcherHTTP = "http"

// Middleware mints a request id, puts it in the context, and records the
// request when it finishes.
//
// This is where correlation begins. Everything else observe records is
// recorded by something that already receives a context — a driver's
// QueryerContext, an slog handler, a cache listener — so putting the id in
// the context here is what makes "every query this request issued" a question
// with an answer.
//
// It is an app.HTTPMiddleware rather than an app.Handler on purpose. The
// router's own middleware runs after pattern matching, so a request that
// matched nothing, or that was refused by something earlier in the chain,
// would go unrecorded — and those are the requests somebody is most often
// trying to explain.
func Middleware(recorder *observe.Recorder) app.HTTPMiddleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if recorder == nil {
				next.ServeHTTP(w, r)
				return
			}

			requestID := observe.NewID()
			ctx := observe.WithRequestID(r.Context(), requestID)
			r = r.WithContext(ctx)

			// Handed to the client so a person reporting a problem can quote
			// the id, and a reader can go straight to that request.
			w.Header().Set("X-Request-Id", requestID)

			recording := &recordingWriter{ResponseWriter: w, status: http.StatusOK}
			started := time.Now()

			defer func() {
				// A panic is the request most worth recording, so the entry
				// is written before the panic continues to whatever recovers
				// it. Re-panicking rather than swallowing: this middleware is
				// not the recoverer and must not quietly become one.
				if recovered := recover(); recovered != nil {
					record(recorder, ctx, r, recording, started, recovered)
					panic(recovered)
				}
				record(recorder, ctx, r, recording, started, nil)
			}()

			next.ServeHTTP(recording, r)
		})
	}
}

func record(recorder *observe.Recorder, ctx context.Context, r *http.Request,
	recording *recordingWriter, started time.Time, recovered any) {

	payload := requestPayload{
		Method:  r.Method,
		Path:    r.URL.Path,
		Query:   r.URL.RawQuery,
		Status:  recording.status,
		Bytes:   recording.written,
		IP:      clientIP(r),
		Headers: observe.RedactHeaders(r.Header),
	}
	if recovered != nil {
		payload.Panicked = true
	}

	level := slog.LevelInfo
	switch {
	case recovered != nil || recording.status >= 500:
		level = slog.LevelError
	case recording.status >= 400:
		level = slog.LevelWarn
	}

	entry := observe.Entry{
		Kind:     observe.KindRequest,
		At:       started,
		Duration: time.Since(started),
		Level:    level,
		Summary:  r.Method + " " + r.URL.Path,
		Watcher:  WatcherHTTP,
	}
	if data, err := json.Marshal(payload); err == nil {
		entry.Data = data
	}
	if recovered != nil {
		entry.Err = "panic: " + stringify(recovered)
	}

	recorder.Record(ctx, entry)
}

type requestPayload struct {
	Method   string              `json:"method"`
	Path     string              `json:"path"`
	Query    string              `json:"query,omitempty"`
	Status   int                 `json:"status"`
	Bytes    int64               `json:"bytes"`
	IP       string              `json:"ip,omitempty"`
	Headers  map[string][]string `json:"headers,omitempty"`
	Panicked bool                `json:"panicked,omitempty"`
}

// recordingWriter captures the status and the size.
//
// It forwards Flush, Hijack, Push and ReadFrom explicitly, and declares
// Unwrap. Both are needed and for different reasons: http.ResponseController
// follows Unwrap, but a type assertion traverses nothing, and plenty of code
// — including tasker's dashboard — asserts directly. Wrapping without these
// is what had every event stream in this framework answering 500 "streaming
// not supported", and a WebSocket upgrade 501.
type recordingWriter struct {
	http.ResponseWriter
	status      int
	written     int64
	wroteHeader bool
}

var (
	_ http.ResponseWriter = (*recordingWriter)(nil)
	_ http.Flusher        = (*recordingWriter)(nil)
	_ http.Hijacker       = (*recordingWriter)(nil)
)

func (w *recordingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *recordingWriter) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.status = status
	w.wroteHeader = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *recordingWriter) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(b)
	w.written += int64(n)
	return n, err
}

func stringify(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case error:
		return typed.Error()
	default:
		encoded, err := json.Marshal(typed)
		if err != nil {
			return "unprintable panic value"
		}
		return string(encoded)
	}
}

func clientIP(r *http.Request) string {
	// Deliberately the peer address, never X-Forwarded-For. The header is
	// client-supplied, and recording it as the client's address would let
	// anybody write whatever they liked into the debugger an operator reads.
	host, _, found := cut(r.RemoteAddr, ':')
	if !found {
		return r.RemoteAddr
	}
	return host
}

func cut(s string, sep byte) (before, after string, found bool) {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == sep {
			return s[:i], s[i+1:], true
		}
	}
	return s, "", false
}
