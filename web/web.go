// Package web serves the dashboard.
//
// One Go file of handlers over a private mux, and one string constant of
// HTML. No go:embed, no build step, no npm — the same arrangement tasker's
// queue dashboard uses, which is what makes a module able to ship a UI at all
// in a framework with no distribution path for frontend assets.
package web

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lemmego/observe"
)

// Server is the dashboard.
//
// Closed until authentication is configured. Entries hold request headers,
// bound query parameters and every statement the application ran, so an
// unauthenticated dashboard is a credential dump with a nice table.
type Server struct {
	recorder    *observe.Recorder
	mux         *http.ServeMux
	middlewares []func(http.Handler) http.Handler
	prefix      string
	authEnabled bool
}

// New returns a dashboard mounted at prefix.
func New(recorder *observe.Recorder, prefix string) *Server {
	s := &Server{
		recorder: recorder,
		mux:      http.NewServeMux(),
		prefix:   strings.TrimRight(prefix, "/"),
	}
	s.routes()
	return s
}

// Use adds middleware without opening the dashboard.
func (s *Server) Use(middleware func(http.Handler) http.Handler) {
	s.middlewares = append(s.middlewares, middleware)
}

// UseAuth adds middleware and opens the dashboard.
//
// The only thing that sets authEnabled, so forgetting to authenticate cannot
// accidentally produce an open dashboard — it produces a closed one.
func (s *Server) UseAuth(middleware func(http.Handler) http.Handler) {
	s.middlewares = append(s.middlewares, middleware)
	s.authEnabled = true
}

// Handler returns the dashboard with its middleware applied.
func (s *Server) Handler() http.Handler {
	var handler http.Handler = s.mux

	if !s.authEnabled {
		handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeError(w, http.StatusUnauthorized,
				"observe: the dashboard has no authentication configured, so it "+
					"is closed. Set Provider.DashboardAuth to open it.")
		})
	}

	for i := len(s.middlewares) - 1; i >= 0; i-- {
		handler = s.middlewares[i](handler)
	}
	return handler
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.Handler().ServeHTTP(w, r)
}

func (s *Server) p(path string) string { return s.prefix + path }

func (s *Server) routes() {
	s.mux.HandleFunc("GET "+s.p("/"), s.handleDashboard)
	s.mux.HandleFunc("GET "+s.p("/api/entries"), s.handleEntries)
	s.mux.HandleFunc("GET "+s.p("/api/requests/{id}"), s.handleRequest)
	s.mux.HandleFunc("GET "+s.p("/api/stats"), s.handleStats)
}

func (s *Server) handleDashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	//nolint:errcheck // a client that hung up is not an error worth reporting.
	fmt.Fprint(w, dashboardHTML)
}

// handleEntries lists entries.
//
// The query decoding is the HTTP half of the one Query type the MCP tools
// also decode into, so a filter is defined once and the two surfaces cannot
// drift.
func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) {
	query, err := decodeQuery(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	store := s.store()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "observe: recording is off")
		return
	}

	entries, err := store.Entries(r.Context(), query)
	if err != nil {
		slog.Error("observe: reading entries for the dashboard", "error", err)
		writeError(w, http.StatusInternalServerError, "observe: entries could not be read")
		return
	}

	var next string
	if len(entries) > 0 {
		next = entries[len(entries)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data":   entries,
		"cursor": next,
		"limit":  observe.ClampLimit(query.Cursor.Limit),
	})
}

// handleRequest returns one request and everything recorded under it, which
// is the view the whole package exists for.
func (s *Server) handleRequest(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "observe: no request id")
		return
	}

	store := s.store()
	if store == nil {
		writeError(w, http.StatusServiceUnavailable, "observe: recording is off")
		return
	}

	entries, err := store.Entries(r.Context(), observe.Query{
		RequestID: id,
		Cursor:    observe.Cursor{Limit: observe.MaxPageSize},
	})
	if err != nil {
		slog.Error("observe: reading a request for the dashboard", "error", err)
		writeError(w, http.StatusInternalServerError, "observe: the request could not be read")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":        entries,
		"repetitions": observe.Repetitions(entries, observe.DefaultNPlusOneThreshold),
	})
}

// handleStats reports what the recorder is doing, including which watchers
// are installed — so an empty table can be told from an unwatched one.
func (s *Server) handleStats(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":    s.recorder.Stats(),
		"watchers": s.recorder.Watchers(),
	})
}

func (s *Server) store() observe.Store {
	if s.recorder == nil {
		return nil
	}
	return s.recorder.Store()
}

// decodeQuery turns a query string into an observe.Query.
func decodeQuery(r *http.Request) (observe.Query, error) {
	values := r.URL.Query()
	query := observe.Query{
		RequestID: values.Get("request"),
		Contains:  values.Get("q"),
		Cursor:    observe.Cursor{BeforeID: values.Get("cursor")},
	}

	for _, kind := range strings.Split(values.Get("kind"), ",") {
		if kind = strings.TrimSpace(kind); kind != "" {
			query.Kinds = append(query.Kinds, observe.Kind(kind))
		}
	}

	if raw := values.Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil {
			return observe.Query{}, fmt.Errorf("observe: limit %q is not a number", raw)
		}
		// Clamped rather than honoured. A limit arriving from a query string
		// is the normal case, and an unbounded one is a way to ask the
		// database for the whole table.
		query.Cursor.Limit = observe.ClampLimit(limit)
	}

	if raw := values.Get("since"); raw != "" {
		since, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return observe.Query{}, fmt.Errorf("observe: since %q is not an RFC3339 time", raw)
		}
		query.Since = since
	}

	if raw := values.Get("level"); raw != "" {
		level, ok := parseLevel(raw)
		if !ok {
			return observe.Query{}, fmt.Errorf("observe: level %q is not one of debug, info, warn, error", raw)
		}
		query.MinLevel = &level
	}

	return query, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Debug("observe: writing a dashboard response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
