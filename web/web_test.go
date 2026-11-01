package web_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/web"
)

func newServer(t *testing.T) (*web.Server, observe.Store) {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	recorder.Watching("sqldriver")
	return web.New(recorder, "/observe"), store
}

// TestTheDashboardIsClosedUntilAuthenticationIsConfigured.
//
// Nil is not "no authentication", it is "no access". These entries hold
// request headers, bound query parameters and every statement the
// application ran, so the failure mode of the opposite default is a
// credential dump behind a nice table.
func TestTheDashboardIsClosedUntilAuthenticationIsConfigured(t *testing.T) {
	server, _ := newServer(t)

	for _, path := range []string{"/observe/", "/observe/api/entries", "/observe/api/stats"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))

		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s answered %d with no auth configured, want 401", path, recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "<table") {
			t.Errorf("%s served the dashboard with no auth configured", path)
		}
	}
}

// TestUseDoesNotOpenTheDashboard, because middleware that is not
// authentication must not be mistakable for it.
func TestUseDoesNotOpenTheDashboard(t *testing.T) {
	server, _ := newServer(t)
	server.Use(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("Use opened the dashboard (answered %d); only UseAuth may", recorder.Code)
	}
}

func TestUseAuthOpensIt(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("the dashboard answered %d with auth configured, want 200", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "<title>observe</title>") {
		t.Error("the dashboard did not render")
	}
}

// TestAnAbsurdLimitIsClamped. A limit arriving from a query string is the
// normal case, and an unbounded one is a way to ask for the whole table.
func TestAnAbsurdLimitIsClamped(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	entries := make([]observe.Entry, 0, observe.MaxPageSize+50)
	for i := range observe.MaxPageSize + 50 {
		entries = append(entries, observe.Entry{
			ID:   "entry-" + itoa(i),
			Kind: observe.KindQuery,
		})
	}
	if err := store.Record(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
		"/observe/api/entries?limit=10000000", nil))

	var body struct {
		Data  []observe.Entry `json:"data"`
		Limit int             `json:"limit"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if len(body.Data) > observe.MaxPageSize {
		t.Errorf("got %d entries for a limit of ten million; it was not clamped",
			len(body.Data))
	}
	if body.Limit > observe.MaxPageSize {
		t.Errorf("the reported limit is %d, want it clamped", body.Limit)
	}
}

// TestABadFilterIsRefusedRatherThanIgnored.
//
// Silently ignoring an unparsable filter shows the unfiltered table, which
// reads as "there is nothing matching" — the one answer a debugger must not
// be given wrongly.
func TestABadFilterIsRefusedRatherThanIgnored(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	for _, query := range []string{"limit=abc", "level=lourd", "since=yesterday"} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet,
			"/observe/api/entries?"+query, nil))

		if recorder.Code != http.StatusBadRequest {
			t.Errorf("?%s answered %d, want 400 — an ignored filter shows the "+
				"unfiltered table and reads as 'nothing matched'", query, recorder.Code)
		}
	}
}

// TestARequestViewCarriesItsRepetitions, which is the finding somebody opens
// a request to look for.
func TestARequestViewCarriesItsRepetitions(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	entries := []observe.Entry{{
		ID: "req", Kind: observe.KindRequest, RequestID: "r1", Summary: "GET /feed",
	}}
	for i := range 20 {
		entries = append(entries, observe.Entry{
			ID:        "q-" + itoa(i),
			Kind:      observe.KindQuery,
			RequestID: "r1",
			Summary:   "SELECT * FROM users WHERE id = " + itoa(i),
		})
	}
	if err := store.Record(context.Background(), entries); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/requests/r1", nil))

	var body struct {
		Data        []observe.Entry      `json:"data"`
		Repetitions []observe.Repetition `json:"repetitions"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v (%s)", err, recorder.Body.String())
	}
	if len(body.Data) != 21 {
		t.Errorf("got %d entries under the request, want 21", len(body.Data))
	}
	if len(body.Repetitions) != 1 || body.Repetitions[0].Count != 20 {
		t.Errorf("the N+1 was not reported: %+v", body.Repetitions)
	}
}

// TestStatsNameTheWatchers, so an empty table can be told from an unwatched
// one.
func TestStatsNameTheWatchers(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/stats", nil))

	var body struct {
		Watchers []string `json:"watchers"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(body.Watchers) == 0 {
		t.Error("stats named no watchers, so an empty table cannot be told " +
			"from one nothing is watching")
	}
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

// TestAnEntryCanBeOpenedOnItsOwn.
//
// An exception's stack and a dump's value are in the payload, and the list
// shows one line of it — so an entry is worth opening even when the request
// view is not what you want.
func TestAnEntryCanBeOpenedOnItsOwn(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	if err := store.Record(context.Background(), []observe.Entry{{
		ID: "entry-1", Kind: observe.KindException, Summary: "panic: nil map",
		Data: []byte(`{"stack":"goroutine 1"}`), Tags: []string{"user:7"},
	}}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/entries/entry-1", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("answered %d", recorder.Code)
	}
	var body struct {
		Data observe.Entry `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Data.Summary != "panic: nil map" {
		t.Errorf("Summary = %q", body.Data.Summary)
	}
	if string(body.Data.Data) != `{"stack":"goroutine 1"}` {
		t.Errorf("the payload did not round-trip: %s", body.Data.Data)
	}
	if len(body.Data.Tags) != 1 || body.Data.Tags[0] != "user:7" {
		t.Errorf("Tags = %v", body.Data.Tags)
	}
}

// An id naming an entry that has since been pruned is ordinary, and a reader
// following a stale link should be told it is gone rather than that something
// broke.
func TestAnUnknownEntryIs404(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/entries/nope", nil))

	if recorder.Code != http.StatusNotFound {
		t.Errorf("answered %d for an unknown entry, want 404", recorder.Code)
	}
}

// Tags filter the list the same way they filter the tail, which is what keeps
// a chip in the UI meaning one thing.
func TestTheListFiltersOnTags(t *testing.T) {
	server, store := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	if err := store.Record(context.Background(), []observe.Entry{
		{ID: "a", Kind: observe.KindQuery, Summary: "theirs", Tags: []string{"user:7"}},
		{ID: "b", Kind: observe.KindQuery, Summary: "somebody else", Tags: []string{"user:8"}},
	}); err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/observe/api/entries?tags=user:7", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("answered %d", recorder.Code)
	}
	if !strings.Contains(recorder.Body.String(), "theirs") {
		t.Error("the tagged entry is missing")
	}
	if strings.Contains(recorder.Body.String(), "somebody else") {
		t.Error("an entry with a different tag was returned")
	}
}

// Pausing is a POST because it changes state — which also means the
// application's CSRF middleware applies when the dashboard is mounted behind
// it, and that is the right default for a button that turns the debugger off.
func TestPausingIsAPostAndIsReportedInStats(t *testing.T) {
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	// A GET does not pause. It answers 200 because "/observe/" is a subtree
	// pattern and anything unmatched under it serves the dashboard — so the
	// assertion is on the state, not on the status, which is the part that
	// would actually be a bug.
	get := httptest.NewRecorder()
	server.ServeHTTP(get, httptest.NewRequest(http.MethodGet, "/observe/api/pause", nil))

	afterGet := httptest.NewRecorder()
	server.ServeHTTP(afterGet, httptest.NewRequest(http.MethodGet, "/observe/api/stats", nil))
	if !strings.Contains(afterGet.Body.String(), `"paused":false`) {
		t.Error("a GET paused recording; a state change must not be reachable " +
			"by a method that CSRF protection does not cover")
	}

	post := httptest.NewRecorder()
	server.ServeHTTP(post, httptest.NewRequest(http.MethodPost, "/observe/api/pause", nil))
	if post.Code != http.StatusOK {
		t.Fatalf("POST answered %d", post.Code)
	}

	stats := httptest.NewRecorder()
	server.ServeHTTP(stats, httptest.NewRequest(http.MethodGet, "/observe/api/stats", nil))
	if !strings.Contains(stats.Body.String(), `"paused":true`) {
		t.Errorf("stats do not report the pause: %s", stats.Body.String())
	}

	resume := httptest.NewRecorder()
	server.ServeHTTP(resume, httptest.NewRequest(http.MethodPost, "/observe/api/pause?resume=1", nil))
	if resume.Code != http.StatusOK {
		t.Fatalf("resume answered %d", resume.Code)
	}
	if !strings.Contains(resume.Body.String(), `"paused":false`) {
		t.Errorf("resume did not resume: %s", resume.Body.String())
	}
}

// The tail is behind the same guard as everything else. A stream that bypassed
// it would be the whole credential dump, live.
func TestTheTailIsClosedWithoutAuthentication(t *testing.T) {
	server, _ := newServer(t)

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/api/tail", nil))

	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("the tail answered %d with no auth configured, want 401", recorder.Code)
	}
}

// TestTheDashboardScriptIsSyntacticallyValid.
//
// The UI is a Go string, so nothing parses it until a browser does — which is
// after a release, in somebody else's tab, with no error anywhere a developer
// is looking. A missing brace would render the page and then do nothing at
// all, which reads as "observe is broken" rather than as a typo.
//
// Crude, and it catches the class of mistake that actually happens when 400
// lines of JavaScript live in a string literal.
func TestTheDashboardScriptIsSyntacticallyValid(t *testing.T) {
	page := servedPage(t)

	open := strings.Index(page, "<script>")
	closing := strings.LastIndex(page, "</script>")
	if open < 0 || closing < open {
		t.Fatal("the page has no script block")
	}
	script := page[open+len("<script>") : closing]

	// A Go raw string cannot contain a backtick, so a template literal is
	// unavailable by construction — worth asserting, because the natural way
	// to write this JavaScript is the way that cannot compile.
	if strings.Contains(script, "`") {
		t.Error("the script contains a backtick, which cannot appear in the " +
			"raw string literal that holds it")
	}

	for _, pair := range []struct {
		name        string
		open, close rune
	}{
		{"braces", '{', '}'},
		{"parentheses", '(', ')'},
		{"brackets", '[', ']'},
	} {
		depth := 0
		for _, char := range script {
			switch char {
			case pair.open:
				depth++
			case pair.close:
				depth--
			}
		}
		if depth != 0 {
			t.Errorf("%s are unbalanced by %d", pair.name, depth)
		}
	}
}

// TestEveryControlTheMarkupDeclaresIsReachedByTheScript.
//
// An element with an id and no handler is a button that does nothing, which
// is worse than an absent button: it looks like a feature.
func TestEveryControlTheMarkupDeclaresIsReachedByTheScript(t *testing.T) {
	page := servedPage(t)

	ids := regexp.MustCompile(`id="([a-zA-Z0-9_-]+)"`).FindAllStringSubmatch(page, -1)
	if len(ids) < 8 {
		t.Fatalf("found %d elements with ids, which is too few to be the real page", len(ids))
	}
	script := page[strings.Index(page, "<script>"):]

	for _, match := range ids {
		id := match[1]
		if !strings.Contains(script, "'"+id+"'") && !strings.Contains(script, `"`+id+`"`) {
			t.Errorf("nothing in the script names #%s, so the element does nothing", id)
		}
	}
}

// TestEveryKindHasAPanel.
//
// A kind added to entry.go with no panel in the sidebar is a kind the
// dashboard silently never shows — the entries are recorded, stored and
// queryable, and invisible. That is the failure this package exists to
// prevent, reproduced in its own UI.
func TestEveryKindHasAPanel(t *testing.T) {
	page := servedPage(t)

	for _, kind := range []observe.Kind{
		observe.KindRequest, observe.KindQuery, observe.KindLog, observe.KindCache,
		observe.KindJob, observe.KindMail, observe.KindException, observe.KindOutbound,
		observe.KindDump, observe.KindNotification, observe.KindView, observe.KindGate,
		observe.KindEvent, observe.KindSchedule,
	} {
		if !strings.Contains(page, "kind:'"+string(kind)+"'") {
			t.Errorf("the sidebar has no panel for %q, so those entries are "+
				"recorded and never shown", kind)
		}
	}
}

func servedPage(t *testing.T) string {
	t.Helper()
	server, _ := newServer(t)
	server.UseAuth(func(next http.Handler) http.Handler { return next })

	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/observe/", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("the dashboard answered %d", recorder.Code)
	}
	return recorder.Body.String()
}

// TestTheScriptDoesNotCompareLevelsNumerically.
//
// slog.Level marshals itself as a string — "INFO", "ERROR", and "ERROR+2"
// for a level between the named ones — so `e.level >= 8` compares a string
// with a number, which is NaN, which is false. The dashboard carried that
// comparison for its whole life and the symptom was silence: a 500 looked
// exactly like a 200 unless it also carried an error string, and every entry
// rendered as DEBUG.
//
// A Go test cannot run the JavaScript, so it guards the shape instead. That
// is worth having precisely because the bug is invisible — nothing throws,
// nothing logs, the page just quietly stops meaning anything.
func TestTheScriptDoesNotCompareLevelsNumerically(t *testing.T) {
	page := servedPage(t)

	for _, bad := range []string{"e.level >=", "e.level >", "e.level <", "entry.level >="} {
		if strings.Contains(page, bad) {
			t.Errorf("the script contains %q. slog.Level arrives as a string, so "+
				"that comparison is always false — route it through levelOf().", bad)
		}
	}
	if !strings.Contains(page, "function levelOf(") {
		t.Error("the script has no levelOf(), so nothing is parsing slog's string levels")
	}
}

// TestTheHeaderIsSticky.
//
// The list is the one thing here that is always long, so a panel title you
// cannot see and a Live button you have to scroll back up to reach are the
// two things a live tail makes worse.
func TestTheHeaderIsSticky(t *testing.T) {
	page := servedPage(t)

	for _, want := range []string{
		".head{position:sticky",
		".bar{position:sticky",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the stylesheet has no rule starting %q", want)
		}
	}
	// Measured rather than guessed: the controls wrap at narrow widths, and a
	// hardcoded offset leaves a gap on one layout and an overlap on the other.
	if !strings.Contains(page, "--headh") || !strings.Contains(page, "setProperty('--headh'") {
		t.Error("the sticky offset is not measured from the header's real height")
	}
}

// TestBothThemesDefineEverySemanticColour.
//
// A light theme that redefines the greys and leaves the severity colours
// alone is a theme where half the page is unreadable — pale-on-pale pills,
// white text on white chips. Every token the dark palette declares has to
// exist in the light one, and this counts them rather than trusting that.
func TestBothThemesDefineEverySemanticColour(t *testing.T) {
	page := servedPage(t)

	root := between(page, ":root{", "}")
	light := between(page, `:root[data-theme="light"]{`, "}")
	if root == "" || light == "" {
		t.Fatal("could not find both palettes in the stylesheet")
	}

	for _, token := range strings.Split(root, ";") {
		name, _, found := strings.Cut(strings.TrimSpace(token), ":")
		if !found || !strings.HasPrefix(name, "--") {
			continue
		}
		if !strings.Contains(light, name+":") {
			t.Errorf("the light palette does not define %s, so that colour keeps "+
				"its dark value on a light page", name)
		}
	}
}

func between(s, open, close string) string {
	start := strings.Index(s, open)
	if start < 0 {
		return ""
	}
	rest := s[start+len(open):]
	end := strings.Index(rest, close)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// TestTheDocumentDoesNotScrollOnADesktop.
//
// macOS rubber-banding drags the whole document past its end, and a
// position:sticky header is positioned against that document — so the header
// detaches and rides the bounce, and the gap it leaves shows the html
// background rather than the page. Clamping the scroll to an inner container
// leaves nothing for the bounce to pull.
//
// overscroll-behavior:contain is the half that is easy to leave out: without
// it a scroll reaching the end of the pane chains back up to the document and
// bounces that instead, which looks exactly like the problem it was meant to
// fix.
func TestTheDocumentDoesNotScrollOnADesktop(t *testing.T) {
	page := servedPage(t)

	for _, want := range []string{
		"html,body{height:100%;overflow:hidden;overscroll-behavior:none}",
		".main{height:100%;overflow-y:auto;overscroll-behavior:contain}",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the stylesheet is missing %q", want)
		}
	}

	// Only above the breakpoint. A phone with a fixed-height pane fights the
	// browser's own disappearing chrome, and the header is static there
	// anyway.
	if !strings.Contains(page, "@media (min-width:861px){") {
		t.Error("the fixed-viewport layout is not behind a desktop media query")
	}

	// html is painted regardless, so a browser honouring none of this still
	// bounces in the theme's colour rather than in white.
	if !strings.Contains(page, "html{background:var(--bg)}") {
		t.Error("html has no background, so an overscroll shows white under a dark page")
	}

	// The pane is the scroll container, so scrolling the window alone would
	// leave a detail view opened halfway down.
	if !strings.Contains(page, "main.scrollTop = 0") {
		t.Error("opening a detail view does not reset the pane's scroll")
	}
}
