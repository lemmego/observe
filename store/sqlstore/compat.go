package sqlstore

import (
	"context"
	"log/slog"

	"github.com/lemmego/observe"
)

func contextWithoutRecording(ctx context.Context) context.Context {
	return observe.WithoutRecording(ctx)
}

// slogLevel converts a stored level back.
//
// slog.Level is an int whose values are stable and documented, so storing the
// number keeps the column orderable — which is what makes "at or above warn"
// an index range rather than a scan over strings.
func slogLevel(value int) slog.Level { return slog.Level(value) }
