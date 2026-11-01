// Package observenotify records what the application notified people about.
//
// It attaches to notify's own event sink rather than decorating the channels,
// and that choice is the whole design. A Channel may also implement
// BulkChannel, Supports, provider.Publisher or io.Closer, and notify
// type-asserts for all four — so a wrapper would have to reproduce every
// combination of them or silently turn the features off. That is the
// http.Hijacker bug this repository has now paid for twice, and the sink
// exists precisely so an observer does not have to risk it.
//
// The cost is correlation: notify.Event carries no context.Context, so an
// entry cannot say which request caused it. What was sent, to whom, on which
// channel and whether it failed are all legible; "what did this request
// notify" is not. Same shape as the cache watcher, and the same fix when it
// comes.
package observenotify

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/lemmego/notify"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "notify"

// notificationPayload is what a notification entry carries in Data.
type notificationPayload struct {
	// Notification is the notification's type name, which is what a reader
	// recognises — "*notifications.Followed".
	Notification string `json:"notification"`
	Channel      string `json:"channel"`

	// Recipient is notify's own pair, kind and id. Not an email address or a
	// phone number: those are the routing details, they live on the channel,
	// and recording them here would put contact details in a debugging store
	// for every notification an application ever sends.
	RecipientKind string `json:"recipient_kind,omitempty"`
	RecipientID   string `json:"recipient_id,omitempty"`

	Correlation string `json:"correlation"`
}

// Sink returns a listener for notify.WithEventSink.
//
//	notify.NewWithOptions(channels, []notify.Option{
//	    notify.WithEventSink(observenotify.Sink()),
//	})
//
// Or, through the provider, which is the normal path:
//
//	&notifyprovider.Provider{
//	    Channels:  channels,
//	    EventSink: observenotify.Sink(),
//	}
//
// EventSink composes with the provider's Events field rather than competing
// with it, so bridging to the application's emitter and watching here are
// not an either/or. That needs notify v0.2.3 or later; before it, Provider
// had no way to take a second sink at all.
//
// It resolves the recorder on each event rather than taking one now, which
// is what makes it safe to construct inside LoadProviders: that slice is
// built in full before any provider runs, so observe.Default() is still nil
// at the point this is written down.
//
// Only Sent and Failed are recorded. Sending is the same delivery announced
// twice, and a store with both would show every notification once for being
// attempted and once for the outcome.
func Sink() notify.Listener {
	return func(event notify.Event) {
		recorder := observe.Default()
		if recorder == nil {
			return
		}

		var (
			payload notificationPayload
			level   = slog.LevelInfo
			failure error
		)

		switch e := event.(type) {
		case notify.Sent:
			payload = notificationPayload{
				Notification:  e.Notification,
				Channel:       e.Channel,
				RecipientKind: e.OwnerKind,
				RecipientID:   e.OwnerID,
			}
		case notify.Failed:
			payload = notificationPayload{
				Notification:  e.Notification,
				Channel:       e.Channel,
				RecipientKind: e.OwnerKind,
				RecipientID:   e.OwnerID,
			}
			level = slog.LevelError
			failure = e.Err
		default:
			// Sending, or a kind notify adds later. Ignored rather than
			// recorded as something unnamed.
			return
		}

		// Said rather than left blank, so a reader stops looking for the
		// request that caused this. See the package documentation.
		payload.Correlation = "unavailable"

		entry := observe.Entry{
			Kind:    observe.KindNotification,
			At:      time.Now(),
			Level:   level,
			Summary: payload.Notification + " via " + payload.Channel,
			Watcher: Watcher,
		}
		if failure != nil {
			entry.Err = failure.Error()
		}
		if data, err := json.Marshal(payload); err == nil {
			entry.Data = data
		}

		// context.Background, because this seam has no context to pass. The
		// entry says so in its payload rather than appearing to have lost
		// one.
		recorder.Record(context.Background(), entry)
		recorder.Watching(Watcher)
	}
}
