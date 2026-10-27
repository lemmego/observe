// Package observemcp serves recorded entries to an AI coding agent.
//
// This is the half of observe that a dashboard cannot be. The question
// "what did that request actually do?" is one an agent debugging your code
// needs answered far more often than a person watching a table does, and
// until now the only thing close was a grep over a rotating log file.
//
// Wire it into the MCP server rather than the other way round:
//
//	&mcp.Provider{Options: []mcp.Option{
//	    mcp.WithTools(observemcp.Tools()),
//	}}
//
// Tools takes no recorder, and that is deliberate: LoadProviders is written
// before anything exists, so a recorder passed there would be the nil one
// observe's provider has not built yet. The tools resolve it when a call
// arrives instead, which also means the ordering between the two providers
// does not matter.
//
// One thing to know before reading an empty answer. `lemmego run mcp:serve`
// is a separate process from the web server, so these tools see only what a
// shared store holds. With observe on an in-memory store the answer is always
// empty, and every tool below says so rather than letting that read as "the
// application did nothing".
package observemcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lemmego/observe"
)

// Tools returns a registrar contributing observe's tools.
func Tools() func(*sdk.Server) {
	t := &tools{}
	return func(server *sdk.Server) {
		t.addRequests(server)
		t.addRequest(server)
		t.addSlowQueries(server)
		t.addRepeatedQueries(server)
		t.addErrors(server)
	}
}

type tools struct{}

// recorder resolves the installed recorder at call time rather than at
// registration time. See the package documentation.
func (t *tools) recorder() *observe.Recorder { return observe.Default() }

func (t *tools) store() (observe.Store, error) {
	recorder := t.recorder()
	if recorder == nil {
		return nil, fmt.Errorf("observe is not recording. Register " +
			"observe/provider.Provider and set OBSERVE_ENABLED=true")
	}
	store := recorder.Store()
	if store == nil {
		return nil, fmt.Errorf("observe has no store")
	}
	return store, nil
}

// note explains an empty result, because an empty result has several causes
// and only one of them is "nothing happened".
func (t *tools) note(found int) string {
	if found > 0 {
		return ""
	}
	watchers := t.recorder().Watchers()
	if len(watchers) == 0 {
		return "\n\nNothing matched, and no watchers are installed — so nothing " +
			"is being recorded at all. This is not evidence that the application is idle."
	}
	return "\n\nNothing matched. Watching: " + strings.Join(watchers, ", ") +
		". If this is unexpected, note that `mcp:serve` is a different process " +
		"from the web server: an in-memory store would be empty here even " +
		"while the web process has entries."
}

// ---------------------------------------------------------------------------

type requestsInput struct {
	Limit  int    `json:"limit,omitempty" jsonschema:"how many requests to return (default 20)"`
	Status string `json:"status,omitempty" jsonschema:"only failing requests when set to 'failed'"`
	Path   string `json:"path,omitempty" jsonschema:"only requests whose path contains this"`
}

type requestSummary struct {
	RequestID  string  `json:"request_id"`
	Summary    string  `json:"summary"`
	DurationMS float64 `json:"duration_ms"`
	At         string  `json:"at"`
	Failed     bool    `json:"failed"`
	Error      string  `json:"error,omitempty"`
}

type requestsOutput struct {
	Requests []requestSummary `json:"requests"`
}

func (t *tools) addRequests(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "observe-requests",
		Description: "Recent HTTP requests this application answered, newest first, with " +
			"how long each took. Start here, then pass a request_id to observe-request " +
			"to see everything that request caused. Filter to failures with status=failed.",
	}, t.requests)
}

func (t *tools) requests(ctx context.Context, _ *sdk.CallToolRequest, in requestsInput) (*sdk.CallToolResult, requestsOutput, error) {
	store, err := t.store()
	if err != nil {
		return failure("%v", err), requestsOutput{}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	query := observe.Query{
		Kinds:    []observe.Kind{observe.KindRequest},
		Contains: in.Path,
		Cursor:   observe.Cursor{Limit: limit},
	}
	if strings.EqualFold(in.Status, "failed") {
		level := levelWarn
		query.MinLevel = &level
	}

	entries, err := store.Entries(ctx, query)
	if err != nil {
		return failure("reading requests: %v", err), requestsOutput{}, nil
	}

	out := requestsOutput{Requests: make([]requestSummary, 0, len(entries))}
	var body strings.Builder
	for _, entry := range entries {
		summary := requestSummary{
			RequestID:  entry.RequestID,
			Summary:    entry.Summary,
			DurationMS: float64(entry.Duration) / float64(time.Millisecond),
			At:         entry.At.UTC().Format(time.RFC3339),
			Failed:     entry.Failed(),
			Error:      entry.Err,
		}
		out.Requests = append(out.Requests, summary)
		fmt.Fprintf(&body, "%-40s %8.1fms  %s%s\n", entry.RequestID,
			summary.DurationMS, entry.Summary, failedMarker(entry))
	}
	return text(body.String() + t.note(len(entries))), out, nil
}

// ---------------------------------------------------------------------------

type requestInput struct {
	RequestID string `json:"request_id" jsonschema:"the id from observe-requests"`
}

type requestEntry struct {
	Kind       string  `json:"kind"`
	Summary    string  `json:"summary"`
	DurationMS float64 `json:"duration_ms"`
	Error      string  `json:"error,omitempty"`
	Watcher    string  `json:"watcher"`
}

type requestOutput struct {
	RequestID   string               `json:"request_id"`
	Entries     []requestEntry       `json:"entries"`
	Repetitions []observe.Repetition `json:"repetitions,omitempty"`
}

func (t *tools) addRequest(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "observe-request",
		Description: "Everything one request caused, in the order it happened: every " +
			"query with its SQL and timing, every log line, every cache operation, " +
			"every job dispatched. This is the tool to reach for when a page is slow " +
			"or wrong and you need to know what it actually did. Repeated statements " +
			"are reported as N+1 candidates.",
	}, t.request)
}

func (t *tools) request(ctx context.Context, _ *sdk.CallToolRequest, in requestInput) (*sdk.CallToolResult, requestOutput, error) {
	store, err := t.store()
	if err != nil {
		return failure("%v", err), requestOutput{}, nil
	}
	if in.RequestID == "" {
		return failure("pass a request_id; observe-requests lists them"), requestOutput{}, nil
	}

	entries, err := store.Entries(ctx, observe.Query{
		RequestID: in.RequestID,
		Cursor:    observe.Cursor{Limit: observe.MaxPageSize},
	})
	if err != nil {
		return failure("reading the request: %v", err), requestOutput{}, nil
	}

	// Oldest first: a request reads as a sequence, where a list reads as a
	// feed.
	reversed := make([]observe.Entry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		reversed = append(reversed, entries[i])
	}

	out := requestOutput{
		RequestID:   in.RequestID,
		Entries:     make([]requestEntry, 0, len(reversed)),
		Repetitions: observe.Repetitions(reversed, observe.DefaultNPlusOneThreshold),
	}

	var body strings.Builder
	for _, entry := range reversed {
		out.Entries = append(out.Entries, requestEntry{
			Kind:       string(entry.Kind),
			Summary:    entry.Summary,
			DurationMS: float64(entry.Duration) / float64(time.Millisecond),
			Error:      entry.Err,
			Watcher:    entry.Watcher,
		})
		fmt.Fprintf(&body, "%-8s %8.2fms  %s%s\n", entry.Kind,
			float64(entry.Duration)/float64(time.Millisecond),
			entry.Summary, failedMarker(entry))
	}

	for _, rep := range out.Repetitions {
		fmt.Fprintf(&body, "\nN+1: %d executions of %s, %s in total.\n",
			rep.Count, rep.Normalised, rep.Total.Round(time.Millisecond))
	}

	return text(body.String() + t.note(len(entries))), out, nil
}

// ---------------------------------------------------------------------------

type slowQueriesInput struct {
	Limit    int `json:"limit,omitempty" jsonschema:"how many to return (default 20)"`
	SinceMin int `json:"since_minutes,omitempty" jsonschema:"only statements from the last N minutes"`
}

type slowQuery struct {
	SQL        string  `json:"sql"`
	DurationMS float64 `json:"duration_ms"`
	RequestID  string  `json:"request_id,omitempty"`
}

type slowQueriesOutput struct {
	Queries []slowQuery `json:"queries"`
}

func (t *tools) addSlowQueries(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "observe-slow-queries",
		Description: "The slowest statements recorded, worst first, each with the request " +
			"it belongs to. Use this to find what to optimise; use observe-repeated-queries " +
			"to find what to batch.",
	}, t.slowQueries)
}

func (t *tools) slowQueries(ctx context.Context, _ *sdk.CallToolRequest, in slowQueriesInput) (*sdk.CallToolResult, slowQueriesOutput, error) {
	store, err := t.store()
	if err != nil {
		return failure("%v", err), slowQueriesOutput{}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}

	query := observe.Query{
		Kinds:  []observe.Kind{observe.KindQuery},
		Cursor: observe.Cursor{Limit: observe.MaxPageSize},
	}
	if in.SinceMin > 0 {
		query.Since = time.Now().Add(-time.Duration(in.SinceMin) * time.Minute)
	}

	entries, err := store.Entries(ctx, query)
	if err != nil {
		return failure("reading statements: %v", err), slowQueriesOutput{}, nil
	}

	// Sorted here rather than in SQL, because the store's contract orders by
	// time — which is what every other reader wants, and what the index
	// serves. A duration index would exist only for this one tool.
	sortByDuration(entries)
	if len(entries) > limit {
		entries = entries[:limit]
	}

	out := slowQueriesOutput{Queries: make([]slowQuery, 0, len(entries))}
	var body strings.Builder
	for _, entry := range entries {
		out.Queries = append(out.Queries, slowQuery{
			SQL:        entry.Summary,
			DurationMS: float64(entry.Duration) / float64(time.Millisecond),
			RequestID:  entry.RequestID,
		})
		fmt.Fprintf(&body, "%8.2fms  %s\n",
			float64(entry.Duration)/float64(time.Millisecond), entry.Summary)
	}
	return text(body.String() + t.note(len(entries))), out, nil
}

// ---------------------------------------------------------------------------

type repeatedInput struct {
	Threshold int `json:"threshold,omitempty" jsonschema:"how many executions of one statement shape counts as a repetition (default 10)"`
	Limit     int `json:"limit,omitempty" jsonschema:"how many requests to examine (default 50)"`
}

type repeatedFinding struct {
	RequestID   string               `json:"request_id"`
	Request     string               `json:"request"`
	Repetitions []observe.Repetition `json:"repetitions"`
}

type repeatedOutput struct {
	Findings []repeatedFinding `json:"findings"`
}

func (t *tools) addRepeatedQueries(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "observe-repeated-queries",
		Description: "Requests that ran the same statement shape many times over — the N+1 " +
			"problem. Statements are grouped with their literals stripped, so a lookup " +
			"repeated with fifty different ids counts as fifty executions of one shape. " +
			"Each finding names the request, so you can open it with observe-request.",
	}, t.repeatedQueries)
}

func (t *tools) repeatedQueries(ctx context.Context, _ *sdk.CallToolRequest, in repeatedInput) (*sdk.CallToolResult, repeatedOutput, error) {
	store, err := t.store()
	if err != nil {
		return failure("%v", err), repeatedOutput{}, nil
	}

	threshold := in.Threshold
	if threshold <= 0 {
		threshold = observe.DefaultNPlusOneThreshold
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}

	requests, err := store.Entries(ctx, observe.Query{
		Kinds:  []observe.Kind{observe.KindRequest},
		Cursor: observe.Cursor{Limit: limit},
	})
	if err != nil {
		return failure("reading requests: %v", err), repeatedOutput{}, nil
	}

	var out repeatedOutput
	var body strings.Builder
	for _, request := range requests {
		if request.RequestID == "" {
			continue
		}
		entries, err := store.Entries(ctx, observe.Query{
			RequestID: request.RequestID,
			Kinds:     []observe.Kind{observe.KindQuery},
			Cursor:    observe.Cursor{Limit: observe.MaxPageSize},
		})
		if err != nil {
			return failure("reading a request's statements: %v", err), repeatedOutput{}, nil
		}

		repetitions := observe.Repetitions(entries, threshold)
		if len(repetitions) == 0 {
			continue
		}
		out.Findings = append(out.Findings, repeatedFinding{
			RequestID:   request.RequestID,
			Request:     request.Summary,
			Repetitions: repetitions,
		})
		fmt.Fprintf(&body, "%s (%s)\n", request.Summary, request.RequestID)
		for _, rep := range repetitions {
			fmt.Fprintf(&body, "    %d x %s  (%s total)\n",
				rep.Count, rep.Normalised, rep.Total.Round(time.Millisecond))
		}
	}

	if len(out.Findings) == 0 {
		return text("No request ran one statement shape more than " +
			itoa(threshold) + " times." + t.note(0)), out, nil
	}
	return text(body.String()), out, nil
}

// ---------------------------------------------------------------------------

type errorsInput struct {
	Limit    int    `json:"limit,omitempty" jsonschema:"how many to return (default 20)"`
	SinceMin int    `json:"since_minutes,omitempty" jsonschema:"only entries from the last N minutes"`
	Contains string `json:"contains,omitempty" jsonschema:"only entries containing this text"`
}

type errorEntry struct {
	Kind      string `json:"kind"`
	Summary   string `json:"summary"`
	Error     string `json:"error,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	At        string `json:"at"`
}

type errorsOutput struct {
	Errors []errorEntry `json:"errors"`
}

func (t *tools) addErrors(server *sdk.Server) {
	sdk.AddTool(server, &sdk.Tool{
		Name: "observe-errors",
		Description: "Recorded entries at error level, of every kind — failed requests, " +
			"failed statements, error logs — newest first and structured. Prefer this " +
			"over last-error, which scans a log file for the substring \"error\" and so " +
			"matches a request to /error-pages. Each carries the request it belongs to.",
	}, t.errors)
}

func (t *tools) errors(ctx context.Context, _ *sdk.CallToolRequest, in errorsInput) (*sdk.CallToolResult, errorsOutput, error) {
	store, err := t.store()
	if err != nil {
		return failure("%v", err), errorsOutput{}, nil
	}

	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	level := levelError
	query := observe.Query{
		MinLevel: &level,
		Contains: in.Contains,
		Cursor:   observe.Cursor{Limit: limit},
	}
	if in.SinceMin > 0 {
		query.Since = time.Now().Add(-time.Duration(in.SinceMin) * time.Minute)
	}

	entries, err := store.Entries(ctx, query)
	if err != nil {
		return failure("reading errors: %v", err), errorsOutput{}, nil
	}

	out := errorsOutput{Errors: make([]errorEntry, 0, len(entries))}
	var body strings.Builder
	for _, entry := range entries {
		out.Errors = append(out.Errors, errorEntry{
			Kind:      string(entry.Kind),
			Summary:   entry.Summary,
			Error:     entry.Err,
			RequestID: entry.RequestID,
			At:        entry.At.UTC().Format(time.RFC3339),
		})
		fmt.Fprintf(&body, "%-8s %s\n", entry.Kind, entry.Summary)
		if entry.Err != "" {
			fmt.Fprintf(&body, "         %s\n", entry.Err)
		}
		if entry.RequestID != "" {
			fmt.Fprintf(&body, "         request %s\n", entry.RequestID)
		}
	}
	return text(body.String() + t.note(len(entries))), out, nil
}

// ---------------------------------------------------------------------------

func failedMarker(entry observe.Entry) string {
	if entry.Failed() {
		return "  <- failed"
	}
	return ""
}

func text(body string) *sdk.CallToolResult {
	if strings.TrimSpace(body) == "" {
		body = "Nothing recorded."
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: body}}}
}

// failure reports a tool-level error rather than a protocol one, so the agent
// reads the reason and can act on it instead of seeing a transport fault.
func failure(format string, args ...any) *sdk.CallToolResult {
	return &sdk.CallToolResult{
		IsError: true,
		Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
