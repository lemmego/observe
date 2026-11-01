package observe

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// WatcherOutbound is the watcher name outbound entries carry.
const WatcherOutbound = "httpclient"

// outboundPayload is what an outbound entry carries in Data.
type outboundPayload struct {
	Method string `json:"method"`
	// URL is the address with its query redacted. See the note in
	// WrapTransport about why the query cannot be kept.
	URL    string `json:"url"`
	Host   string `json:"host"`
	Status int    `json:"status"`

	// Correlation says why an entry has no request id, when it has none, so
	// a reader stops looking for a cause that was never recorded.
	Correlation string `json:"correlation,omitempty"`
}

// WrapTransport records the HTTP requests a client makes.
//
//	client := &http.Client{Transport: observe.WrapTransport(nil)}
//
// A nil base means http.DefaultTransport, so the zero-configuration case is
// the one-liner above.
//
// This lives in the root, beside Wrap and RecordQuery, for the same reason
// they do: it is database/sql-free, framework-free and standard-library only,
// so a program instruments its outgoing calls without importing anything of
// the application's. It is also the only way to see a third party's latency,
// which is as often the cause of a slow page as a slow query and was
// previously invisible.
//
// # What it does not record
//
// No request body, no response body, and no query string. The first two are
// the rule the whole package follows — a body is where a credential lives.
// The query string is the same rule applied where people forget it: an API
// key in ?access_token= is extremely common, and the redactor works on
// key-value pairs it can parse, so a URL carrying one in a path segment or
// an unusual encoding would survive it. Recording the path and dropping the
// query is the honest line.
//
// Headers are not recorded either, which is deliberate and not an oversight:
// an outgoing request's headers are nearly all Authorization.
func WrapTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &observedTransport{base: base}
}

type observedTransport struct {
	base http.RoundTripper
}

// A compile-time assertion, because this is the class of bug a declaration
// cannot get wrong and a type assertion elsewhere can.
var _ http.RoundTripper = (*observedTransport)(nil)

func (t *observedTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	recorder := Default()
	if recorder == nil {
		return t.base.RoundTrip(r)
	}

	started := time.Now()
	res, err := t.base.RoundTrip(r)
	took := time.Since(started)

	payload := outboundPayload{
		Method: r.Method,
		URL:    redactedURL(r.URL),
		Host:   r.URL.Host,
	}
	if res != nil {
		payload.Status = res.StatusCode
	}

	ctx := r.Context()
	if RequestIDFrom(ctx) == "" {
		// Said rather than left blank. An outbound call made from a
		// background goroutine has no request behind it, which is ordinary;
		// one made during a request that lost its context is not, and the
		// two look identical without this.
		payload.Correlation = "unavailable"
	}

	entry := Entry{
		Kind:     KindOutbound,
		At:       started,
		Duration: took,
		Level:    slog.LevelInfo,
		Summary:  r.Method + " " + payload.URL,
		Watcher:  WatcherOutbound,
	}
	switch {
	case err != nil:
		entry.Level = slog.LevelError
		entry.Err = err.Error()
	case payload.Status >= 500:
		entry.Level = slog.LevelError
	case payload.Status >= 400:
		entry.Level = slog.LevelWarn
	}
	if data, marshalErr := json.Marshal(payload); marshalErr == nil {
		entry.Data = data
	}

	recorder.Record(ctx, entry)
	recorder.Watching(WatcherOutbound)

	return res, err
}

// redactedURL renders a URL without its query.
//
// The query goes entirely rather than being redacted key by key: a redactor
// only withholds what it recognises, and an API key arrives under too many
// names to recognise them all.
func redactedURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	clean := *u
	clean.RawQuery = ""
	clean.Fragment = ""
	// A URL may carry credentials in its authority, and those have a name
	// the redactor does know.
	clean.User = nil

	rendered := clean.String()
	if u.RawQuery != "" {
		rendered += "?…"
	}
	return rendered
}
