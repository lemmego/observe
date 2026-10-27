package observemcp

import (
	"log/slog"
	"sort"
	"strconv"

	"github.com/lemmego/observe"
)

const (
	levelWarn  = slog.LevelWarn
	levelError = slog.LevelError
)

// sortByDuration puts the slowest first.
//
// Done here rather than in SQL because the store's contract orders by time,
// which is what every other reader wants and what the index serves. An index
// on duration would exist for this one tool and be maintained on every write.
func sortByDuration(entries []observe.Entry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Duration > entries[j].Duration
	})
}

func itoa(n int) string { return strconv.Itoa(n) }
