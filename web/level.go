package web

import (
	"log/slog"
	"strings"
)

// parseLevel reads a level name.
//
// Its own function rather than slog.Level.UnmarshalText, which accepts an
// offset syntax like "WARN+2" that nobody is going to type into a filter and
// that would make an invalid filter look valid.
func parseLevel(name string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return 0, false
	}
}
