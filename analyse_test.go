package observe_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/lemmego/observe"
)

func queryEntry(request, statement string, took time.Duration) observe.Entry {
	data, _ := json.Marshal(map[string]any{"sql": statement, "rows": -1})
	return observe.Entry{
		ID:        observe.NewID(),
		Kind:      observe.KindQuery,
		RequestID: request,
		Summary:   statement,
		Duration:  took,
		Data:      data,
	}
}

// TestAnNPlusOneIsDetected is the question this package exists to answer.
//
// Verified by lowering the loop below the threshold and watching it fail.
func TestAnNPlusOneIsDetected(t *testing.T) {
	entries := []observe.Entry{
		queryEntry("req-1", "SELECT * FROM posts LIMIT 25", 4*time.Millisecond),
	}
	// The classic shape: one query for the list, then one per row.
	for i := range 25 {
		entries = append(entries, queryEntry("req-1",
			"SELECT * FROM users WHERE id = "+itoa(i), time.Millisecond))
	}

	found := observe.Repetitions(entries, observe.DefaultNPlusOneThreshold)
	if len(found) != 1 {
		t.Fatalf("got %d repetitions, want exactly the author lookup: %+v", len(found), found)
	}

	rep := found[0]
	if rep.Count != 25 {
		t.Errorf("Count = %d, want 25", rep.Count)
	}
	if rep.Total != 25*time.Millisecond {
		t.Errorf("Total = %v, want 25ms; without it a reader cannot tell a "+
			"repetition worth fixing from one that costs nothing", rep.Total)
	}
	if rep.Normalised != "SELECT * FROM users WHERE id = ?" {
		t.Errorf("Normalised = %q; the literals were not stripped, so the "+
			"statements would never have grouped", rep.Normalised)
	}
	if rep.Example == rep.Normalised {
		t.Error("Example should carry a real statement, so the values are visible")
	}
}

// TestALoopBelowTheThresholdIsNotReported, because a report you learn to
// ignore is worse than none.
func TestALoopBelowTheThresholdIsNotReported(t *testing.T) {
	var entries []observe.Entry
	for i := range 3 {
		entries = append(entries, queryEntry("req-1",
			"SELECT * FROM settings WHERE key = "+itoa(i), time.Millisecond))
	}

	if found := observe.Repetitions(entries, observe.DefaultNPlusOneThreshold); len(found) != 0 {
		t.Errorf("three executions were reported as a repetition: %+v", found)
	}
}

// TestPlaceholderStylesGroupTogether, so the same statement prepared by two
// drivers is one shape rather than two.
func TestPlaceholderStylesGroupTogether(t *testing.T) {
	statements := []string{
		"SELECT * FROM users WHERE id = $1",
		"SELECT * FROM users WHERE id = ?",
		"SELECT * FROM users WHERE id = 42",
		"SELECT  *  FROM users\n  WHERE id = 7",
	}
	first := observe.Fingerprint(statements[0])
	for _, statement := range statements[1:] {
		if got := observe.Fingerprint(statement); got != first {
			t.Errorf("%q fingerprinted as %s, want %s — the same statement in "+
				"two placeholder styles must be one shape", statement, got, first)
		}
	}
}

// TestAQuotedLiteralIsStrippedWhateverIsInside, because a literal containing
// a quote or a number is exactly where a naive stripper breaks and starts
// mis-grouping.
func TestAQuotedLiteralIsStrippedWhateverIsInside(t *testing.T) {
	cases := [][2]string{
		{"SELECT * FROM t WHERE name = 'ada'", "SELECT * FROM t WHERE name = ?"},
		{"SELECT * FROM t WHERE name = 'o''brien'", "SELECT * FROM t WHERE name = ?"},
		{`SELECT * FROM t WHERE name = 'it\'s'`, "SELECT * FROM t WHERE name = ?"},
		{"SELECT * FROM t WHERE name = 'has 42 digits'", "SELECT * FROM t WHERE name = ?"},
	}
	for _, tc := range cases {
		if got := observe.NormaliseSQL(tc[0]); got != tc[1] {
			t.Errorf("NormaliseSQL(%q) = %q, want %q", tc[0], got, tc[1])
		}
	}
}

// TestTransactionBoundariesAreNotRepetitions. A request opening ten
// transactions commits ten times, which says nothing about N+1 and would
// drown the real finding.
func TestTransactionBoundariesAreNotRepetitions(t *testing.T) {
	var entries []observe.Entry
	for range 20 {
		entries = append(entries, queryEntry("req-1", "COMMIT", time.Microsecond))
	}
	if found := observe.Repetitions(entries, 2); len(found) != 0 {
		t.Errorf("transaction boundaries were reported as a repetition: %+v", found)
	}
}

// TestRepetitionsAreOrderedWorstFirst, so a reader's eye lands on the one to fix.
func TestRepetitionsAreOrderedWorstFirst(t *testing.T) {
	var entries []observe.Entry
	for range 5 {
		entries = append(entries, queryEntry("req-1", "SELECT 1 FROM a WHERE x = 1", time.Millisecond))
	}
	for range 30 {
		entries = append(entries, queryEntry("req-1", "SELECT 1 FROM b WHERE y = 1", time.Millisecond))
	}

	found := observe.Repetitions(entries, 2)
	if len(found) != 2 {
		t.Fatalf("got %d repetitions, want 2", len(found))
	}
	if found[0].Count != 30 {
		t.Errorf("the worst repetition is second; ordering is by count then time")
	}
}
