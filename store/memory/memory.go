// Package memory keeps entries in this process, bounded.
//
// It is what the contract suite runs against and what a single-process
// development setup can use without a migration. It is not what an
// application reading entries over MCP should use: `lemmego run mcp:serve`
// boots a second process, and entries recorded by the web process are not in
// this one's memory. That asymmetry is why observe/store/sqlstore exists.
package memory

import (
	"container/list"
	"context"
	"strings"
	"sync"
	"time"

	"github.com/lemmego/observe"
)

// DefaultCapacity is how many entries are kept before the oldest go.
const DefaultCapacity = 10000

// Store holds the most recent entries, discarding the oldest.
//
// Bounded, and that is the point rather than a detail. Entries are keyed on
// request ids and statements, which are influenced by whoever is sending
// requests, so an unbounded map here would be a memory-exhaustion vector in
// the component meant to help you debug one.
type Store struct {
	mu    sync.RWMutex
	byID  map[string]*list.Element
	order *list.List // newest at Front
	max   int

	now func() time.Time
}

var _ observe.Store = (*Store)(nil)

// New returns a store holding at most DefaultCapacity entries.
func New() *Store { return NewWithCapacity(DefaultCapacity) }

// NewWithCapacity returns a store holding at most max entries.
func NewWithCapacity(max int) *Store {
	if max <= 0 {
		max = DefaultCapacity
	}
	return &Store{
		byID:  make(map[string]*list.Element),
		order: list.New(),
		max:   max,
		now:   time.Now,
	}
}

// Record stores entries, evicting the oldest once full.
func (s *Store) Record(_ context.Context, entries []observe.Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, entry := range entries {
		if entry.ID == "" {
			entry.ID = observe.NewID()
		}
		if entry.At.IsZero() {
			entry.At = s.now()
		}
		if existing, ok := s.byID[entry.ID]; ok {
			existing.Value = entry
			continue
		}
		s.byID[entry.ID] = s.order.PushFront(entry)

		for s.order.Len() > s.max {
			oldest := s.order.Back()
			if oldest == nil {
				break
			}
			s.order.Remove(oldest)
			delete(s.byID, oldest.Value.(observe.Entry).ID)
		}
	}
	return nil
}

// Entries returns matching entries, newest first.
func (s *Store) Entries(_ context.Context, q observe.Query) ([]observe.Entry, error) {
	limit := observe.ClampLimit(q.Cursor.Limit)

	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]observe.Entry, 0, limit)
	skipping := q.Cursor.BeforeID != ""

	for element := s.order.Front(); element != nil; element = element.Next() {
		entry := element.Value.(observe.Entry)

		// The cursor is exclusive: paging resumes *after* the named entry, or
		// the dashboard shows a duplicate at every page boundary.
		if skipping {
			if entry.ID == q.Cursor.BeforeID {
				skipping = false
			}
			continue
		}
		if !matches(entry, q) {
			continue
		}
		out = append(out, entry)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// Entry returns one entry.
func (s *Store) Entry(_ context.Context, id string) (observe.Entry, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	element, ok := s.byID[id]
	if !ok {
		return observe.Entry{}, observe.ErrNoEntry
	}
	return element.Value.(observe.Entry), nil
}

// Prune drops entries recorded before a cutoff.
func (s *Store) Prune(_ context.Context, before time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var removed int64
	for element := s.order.Back(); element != nil; {
		entry := element.Value.(observe.Entry)
		previous := element.Prev()
		if entry.At.Before(before) {
			s.order.Remove(element)
			delete(s.byID, entry.ID)
			removed++
		}
		element = previous
	}
	return removed, nil
}

// Len reports how many entries are held, for a test.
func (s *Store) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.order.Len()
}

func matches(entry observe.Entry, q observe.Query) bool {
	if len(q.Kinds) > 0 && !slicesContains(q.Kinds, entry.Kind) {
		return false
	}
	if q.MinLevel != nil && entry.Level < *q.MinLevel {
		return false
	}
	if q.RequestID != "" && entry.RequestID != q.RequestID {
		return false
	}
	if !q.Since.IsZero() && entry.At.Before(q.Since) {
		return false
	}
	if !q.Until.IsZero() && entry.At.After(q.Until) {
		return false
	}
	if q.Contains != "" {
		needle := strings.ToLower(q.Contains)
		if !strings.Contains(strings.ToLower(entry.Summary), needle) &&
			!strings.Contains(strings.ToLower(string(entry.Data)), needle) &&
			!strings.Contains(strings.ToLower(entry.Err), needle) {
			return false
		}
	}
	return true
}

func slicesContains(kinds []observe.Kind, want observe.Kind) bool {
	for _, kind := range kinds {
		if kind == want {
			return true
		}
	}
	return false
}
