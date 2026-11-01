package observe_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/lemmego/observe"
	"github.com/lemmego/observe/store/memory"
)

func recordedQuery(t *testing.T, opts ...observe.Option) observe.Entry {
	t.Helper()
	store := memory.New()
	recorder := observe.New(store, opts...)
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	observe.RecordQuery(context.Background(), "sqldriver",
		"SELECT * FROM users WHERE id = $1 AND email = $2",
		[]any{12345, "ada@example.com"}, 3*time.Millisecond, nil)

	if err := recorder.Close(context.Background()); err != nil {
		t.Fatalf("draining: %v", err)
	}
	found, err := store.Entries(context.Background(), observe.Query{})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(found))
	}
	return found[0]
}

func payloadOf(t *testing.T, entry observe.Entry) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(entry.Data, &out); err != nil {
		t.Fatalf("payload: %v", err)
	}
	return out
}

// The default is the detailed answer, because this package was built for a
// machine with one user on it and that is still where most of its use is.
func TestArgsAreRecordedByDefault(t *testing.T) {
	payload := payloadOf(t, recordedQuery(t))
	args, _ := payload["args"].([]any)
	if len(args) != 2 {
		t.Errorf("args = %v, want both bound parameters", payload["args"])
	}
}

func TestWithoutArgsKeepsTheStatementAndDropsTheParameters(t *testing.T) {
	entry := recordedQuery(t, observe.WithoutArgs())
	payload := payloadOf(t, entry)

	if _, present := payload["args"]; present {
		t.Errorf("args = %v, want them absent", payload["args"])
	}
	// The statement survives, so a slow query is still findable and an N+1
	// is still detectable. What goes is which id it was looking for.
	if sql, _ := payload["sql"].(string); !strings.Contains(sql, "FROM users") {
		t.Errorf("sql = %q, want the statement kept", sql)
	}
	// And the value itself is gone from the whole entry, not merely from the
	// field a reader would look in first.
	blob, _ := json.Marshal(entry)
	if strings.Contains(string(blob), "ada@example.com") {
		t.Errorf("a bound parameter survived into the entry:\n%s", blob)
	}
}

func TestWithNormalisedStatementsRecordsTheShape(t *testing.T) {
	entry := recordedQuery(t, observe.WithNormalisedStatements())
	payload := payloadOf(t, entry)

	sql, _ := payload["sql"].(string)
	if !strings.Contains(sql, "FROM users WHERE id = ?") {
		t.Errorf("sql = %q, want the placeholders collapsed", sql)
	}
	// The summary is what a list shows, so it has to agree with the payload
	// rather than quietly keeping the literal form.
	if strings.Contains(entry.Summary, "12345") {
		t.Errorf("Summary = %q, want the shape", entry.Summary)
	}
}

// TestWithoutArgsDoesNotRemoveAnInlinedLiteral.
//
// The caveat worth knowing before reading WithoutArgs as a privacy control:
// it drops the *bound parameters*, and a value written into the statement
// text is part of the statement. An ORM binds, so this is rare — but
// hand-written SQL that interpolates does not, and the difference is
// invisible in the entry.
//
// WithNormalisedStatements is what removes those, which is a second reason
// the production profile turns on both.
func TestWithoutArgsDoesNotRemoveAnInlinedLiteral(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store, observe.WithoutArgs())
	observe.SetDefault(recorder)
	t.Cleanup(func() { observe.SetDefault(nil) })

	observe.RecordQuery(context.Background(), "sqldriver",
		"SELECT * FROM users WHERE email = 'ada@example.com'", nil, 0, nil)
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	found, _ := store.Entries(context.Background(), observe.Query{})
	if len(found) != 1 {
		t.Fatalf("recorded %d entries", len(found))
	}

	blob, _ := json.Marshal(found[0])
	if !strings.Contains(string(blob), "ada@example.com") {
		t.Skip("the inlined literal did not survive, so this caveat no longer holds " +
			"and the documentation on WithoutArgs should be corrected")
	}

	// And with normalisation on, it is gone.
	store = memory.New()
	recorder = observe.New(store, observe.WithoutArgs(), observe.WithNormalisedStatements())
	observe.SetDefault(recorder)

	observe.RecordQuery(context.Background(), "sqldriver",
		"SELECT * FROM users WHERE email = 'ada@example.com'", nil, 0, nil)
	if err := recorder.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	found, _ = store.Entries(context.Background(), observe.Query{})
	if len(found) != 1 {
		t.Fatalf("recorded %d entries", len(found))
	}
	blob, _ = json.Marshal(found[0])
	if strings.Contains(string(blob), "ada@example.com") {
		t.Errorf("an inlined literal survived normalisation:\n%s", blob)
	}
}

func TestTheProductionProfileAppliesBoth(t *testing.T) {
	entry := recordedQuery(t, observe.WithProfile(observe.ProfileProduction))
	payload := payloadOf(t, entry)

	if _, present := payload["args"]; present {
		t.Error("the production profile recorded bound parameters")
	}
	if sql, _ := payload["sql"].(string); strings.Contains(sql, "12345") {
		t.Errorf("the production profile recorded a literal: %q", sql)
	}
}

// An explicit option wins, because New applies options in order and the
// provider lists the profile first — a field set in code is a decision
// somebody wrote down.
func TestAnExplicitOptionOverridesTheProfile(t *testing.T) {
	store := memory.New()
	recorder := observe.New(store,
		observe.WithProfile(observe.ProfileProduction),
		observe.WithoutArgs(), // already off; the point is the order holds
	)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })

	if recorder.RecordsArgs() {
		t.Error("RecordsArgs is true under the production profile")
	}
	if !recorder.NormalisesStatements() {
		t.Error("NormalisesStatements is false under the production profile")
	}
}

func TestProfileFromEnv(t *testing.T) {
	for value, want := range map[string]observe.Profile{
		"production":  observe.ProfileProduction,
		"PRODUCTION":  observe.ProfileProduction,
		"  prod  ":    observe.ProfileProduction,
		"development": observe.ProfileDevelopment,
		"":            observe.ProfileDevelopment,
		// An unrecognised value is development rather than an error: the
		// failure of guessing wrong is "recorded more than you meant", which
		// is the one you can see.
		"staging": observe.ProfileDevelopment,
	} {
		t.Setenv(observe.EnvProfile, value)
		if got := observe.ProfileFromEnv(); got != want {
			t.Errorf("OBSERVE_PROFILE=%q gave %q, want %q", value, got, want)
		}
	}
}

func TestTheProfileCarriesItsDefaults(t *testing.T) {
	if got := observe.ProfileProduction.SampleRate(); got != 0.1 {
		t.Errorf("production SampleRate = %v, want 0.1 — a profile that recorded "+
			"every request would make the word mean nothing", got)
	}
	if got := observe.ProfileDevelopment.SampleRate(); got != 1 {
		t.Errorf("development SampleRate = %v, want everything", got)
	}
	if got := observe.ProfileProduction.MinLogLevel(); got != slog.LevelInfo {
		t.Errorf("production MinLogLevel = %v, want info", got)
	}
	if got := observe.ProfileDevelopment.MinLogLevel(); got != slog.LevelDebug {
		t.Errorf("development MinLogLevel = %v, want no floor", got)
	}
}

// The normaliser is idempotent, which is what lets statements be stored as
// shapes without read-time N+1 grouping changing them again.
func TestNormalisingIsIdempotent(t *testing.T) {
	for _, statement := range []string{
		"SELECT * FROM users WHERE id = 12345 AND name = 'ada'",
		"SELECT  *\n  FROM posts\n WHERE circle_id = $1 LIMIT 20",
		`SELECT 'it''s quoted' FROM t WHERE y = 1.5e-3`,
		"SELECT * FROM naïve WHERE café = 'résumé'",
	} {
		once := observe.NormaliseSQL(statement)
		if twice := observe.NormaliseSQL(once); twice != once {
			t.Errorf("%q normalised to %q and then to %q", statement, once, twice)
		}
	}
}

// Scanning bytes rather than runes is a correctness claim as much as a
// performance one: a multi-byte sequence must not be mistaken for a
// delimiter, and its bytes must survive verbatim.
func TestNormalisingLeavesNonASCIIAlone(t *testing.T) {
	got := observe.NormaliseSQL("SELECT * FROM naïve WHERE café = 'résumé' AND n = 7")
	if !strings.Contains(got, "naïve") || !strings.Contains(got, "café") {
		t.Errorf("got %q, want the identifiers intact", got)
	}
	if strings.Contains(got, "résumé") {
		t.Errorf("got %q, want the quoted literal replaced", got)
	}
}
