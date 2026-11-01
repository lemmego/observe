package sqlstore

import (
	"context"
	"log/slog"
	"strings"

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

// tagSeparator wraps and separates tags in the packed column.
//
// Leading and trailing as well as between, so a LIKE can anchor on both
// sides of a tag: without the wrapping, matching ",user:7," would miss a tag
// that is first or last in the list, and matching "user:7" without anchors
// would also match "user:70".
const tagSeparator = ","

// encodeTags packs tags into one column value.
//
// One column rather than a join table. The trade is stated where the filter
// is built: a substring match cannot use an index, and retention is what
// bounds the scan. A join table would be portable and indexed and would also
// double the write path for every entry — which is on the hot side of a
// recorder whose first promise is that it never slows a request.
func encodeTags(tags []string) string {
	tags = observe.NormaliseTags(tags)
	if len(tags) == 0 {
		return ""
	}
	return tagSeparator + strings.Join(tags, tagSeparator) + tagSeparator
}

// decodeTags unpacks the column.
func decodeTags(packed string) []string {
	packed = strings.Trim(packed, tagSeparator)
	if packed == "" {
		return nil
	}
	return strings.Split(packed, tagSeparator)
}
