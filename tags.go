package observe

import (
	"context"
	"strings"
)

// Tag limits.
//
// Bounded for the same reason the memory store is bounded: tags are how an
// application labels an entry with something it knows about the request, and
// what it knows usually came from the request. An unbounded set keyed on
// anything a client influences is a memory-exhaustion vector in the component
// meant to help you debug one — and these strings are also written to a
// column, so an unbounded one is a row that will not insert.
const (
	// MaxTags is how many tags one entry may carry. Further tags are dropped.
	MaxTags = 12

	// MaxTagLength is how long one tag may be. A longer one is truncated
	// rather than dropped, because a truncated "user:12345…" still groups
	// usefully where a missing tag groups not at all.
	MaxTagLength = 64
)

// TagFailed is added to every entry that describes something that went wrong.
//
// Derived rather than supplied, because Entry already knows: Failed() reads
// the level and the error text. It exists as a tag as well as a level so one
// filter spans kinds — "everything that broke on this request" is one tag
// where it is otherwise a level filter that a warn-level gate denial also
// satisfies.
const TagFailed = "failed"

// tagSeparator is what a store may use to pack tags into one column.
//
// Exported through NormaliseTags rather than directly: a tag containing the
// separator would make `LIKE '%,user:7,%'` match the wrong thing, so it is
// replaced on the way in and no store has to think about it.
const tagSeparator = ","

// NormaliseTags returns tags fit to store: trimmed, de-duplicated, bounded,
// and free of the separator a store packs them with.
//
// It returns nil for an empty result rather than an empty slice, so an entry
// with no tags marshals without the field.
func NormaliseTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}

	out := make([]string, 0, min(len(tags), MaxTags))
	seen := make(map[string]struct{}, len(tags))

	for _, tag := range tags {
		tag = strings.TrimSpace(strings.ReplaceAll(tag, tagSeparator, " "))
		if tag == "" {
			continue
		}
		if len(tag) > MaxTagLength {
			tag = tag[:MaxTagLength]
		}
		if _, already := seen[tag]; already {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
		if len(out) == MaxTags {
			break
		}
	}

	if len(out) == 0 {
		return nil
	}
	return out
}

// tagsContextKey is where request-scoped tags live.
type tagsContextKey struct{}

// WithTags labels everything recorded under this context.
//
// This is the half of tagging that makes it work. A watcher can only tag what
// it knows, and no watcher knows who is signed in — so "everything this user
// did" would mean teaching every watcher about authentication, and would
// still miss the ones added later. Putting the tag on the context once, in a
// middleware, labels the request entry, its queries, its cache lookups, its
// gate checks and anything a watcher adds next year:
//
//	ctx = observe.WithTags(r.Context(), "user:"+id, "tenant:"+tenant)
//
// Additive: tags already on the context are kept, so two middlewares each
// adding one do not fight.
func WithTags(ctx context.Context, tags ...string) context.Context {
	normalised := NormaliseTags(append(TagsFrom(ctx), tags...))
	if len(normalised) == 0 {
		return ctx
	}
	return context.WithValue(ctx, tagsContextKey{}, normalised)
}

// TagsFrom returns the tags carried by a context.
func TagsFrom(ctx context.Context) []string {
	if ctx == nil {
		return nil
	}
	tags, _ := ctx.Value(tagsContextKey{}).([]string)
	return tags
}

// HasTags reports whether an entry carries every one of the given tags.
//
// Every one rather than any, because tags are a narrowing filter: a reader
// asking for `user:7` and `failed` wants what that user broke, not everything
// that user did plus everything that broke.
func HasTags(entry Entry, want []string) bool {
	for _, tag := range want {
		found := false
		for _, has := range entry.Tags {
			if has == tag {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
