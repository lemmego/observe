package observe

import (
	"encoding/json"
	"sort"
	"time"
)

// DefaultNPlusOneThreshold is how many executions of one statement shape
// under a single request is suspicious.
//
// Ten rather than two or three, because a legitimate loop over a small fixed
// set is common and reporting it would train you to ignore the report. The
// classic N+1 — a list page reading one relation per row — produces as many
// as the page has rows, so it clears ten on any page worth paginating.
const DefaultNPlusOneThreshold = 10

// Repetition is one statement shape executed more than once under a request.
type Repetition struct {
	// Fingerprint identifies the shape.
	Fingerprint string `json:"fingerprint"`

	// Normalised is the statement with its literals replaced, which is what
	// was actually grouped on.
	Normalised string `json:"normalised"`

	// Example is one of the real statements, so the values are visible.
	Example string `json:"example"`

	Count int `json:"count"`

	// Total is the time all the executions took together. This is the number
	// that says whether the repetition matters: fifty statements costing a
	// millisecond each is worth fixing, fifty costing a microsecond is not.
	Total time.Duration `json:"total"`
}

// Repetitions groups a request's query entries by statement shape and returns
// those executed at least min times, worst first.
//
// Read-time analysis, so recording stays cheap: a request pays nothing for
// this, and the threshold can change without re-recording anything.
func Repetitions(entries []Entry, min int) []Repetition {
	if min < 2 {
		min = 2
	}

	type group struct {
		normalised string
		example    string
		count      int
		total      time.Duration
	}
	groups := make(map[string]*group)

	for _, entry := range entries {
		if entry.Kind != KindQuery {
			continue
		}
		statement := entry.Summary
		if payload, ok := decodeSQL(entry); ok && payload.SQL != "" {
			statement = payload.SQL
		}
		// A transaction boundary repeats by nature and says nothing about
		// N+1, so it is not a repetition worth reporting.
		if isTransactionBoundary(statement) {
			continue
		}

		key := Fingerprint(statement)
		g := groups[key]
		if g == nil {
			g = &group{normalised: NormaliseSQL(statement), example: statement}
			groups[key] = g
		}
		g.count++
		g.total += entry.Duration
	}

	out := make([]Repetition, 0, len(groups))
	for key, g := range groups {
		if g.count < min {
			continue
		}
		out = append(out, Repetition{
			Fingerprint: key,
			Normalised:  g.normalised,
			Example:     g.example,
			Count:       g.count,
			Total:       g.total,
		})
	}

	// Worst first, by count then by time, so the ordering is stable for a
	// test and useful for a reader.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		if out[i].Total != out[j].Total {
			return out[i].Total > out[j].Total
		}
		return out[i].Fingerprint < out[j].Fingerprint
	})
	return out
}

func decodeSQL(entry Entry) (sqlPayload, bool) {
	if len(entry.Data) == 0 {
		return sqlPayload{}, false
	}
	var payload sqlPayload
	if err := json.Unmarshal(entry.Data, &payload); err != nil {
		return sqlPayload{}, false
	}
	return payload, true
}

func isTransactionBoundary(statement string) bool {
	switch collapseWhitespace(statement) {
	case "COMMIT", "ROLLBACK", "BEGIN":
		return true
	}
	return false
}
