package export

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/lemmego/observe"
)

// Source identifies where entries came from.
//
// Fleet telemetry is incoherent without it: two instances' entries are
// indistinguishable, so "which host is slow" and "did the new release do
// this" are unaskable. It rides in the envelope once per batch rather than
// once per entry, because it is the same for all of them.
type Source struct {
	Service     string `json:"service,omitempty"`
	Environment string `json:"environment,omitempty"`
	Release     string `json:"release,omitempty"`
	Host        string `json:"host,omitempty"`
	Instance    string `json:"instance,omitempty"`
}

// JSONL writes one entry per line to a writer.
//
// The sidecar escape hatch, and the thing to test a collector against: point
// it at a file and let something else tail, rotate or ship it. Line-oriented
// because that is what makes a file greppable and tailable, which is the
// entire reason to choose a file.
//
// The writer is used from one goroutine — the exporter's — but is guarded
// anyway, because a caller passing os.Stdout is sharing it with everything
// else in the process.
//
// It pairs with lemmego/log's rotating writer, which is an io.Writer, so
// rotation is somebody else's solved problem rather than this package's.
func JSONL(w io.Writer) Transport {
	return &jsonlTransport{w: w}
}

type jsonlTransport struct {
	mu sync.Mutex
	w  io.Writer
}

func (t *jsonlTransport) Send(_ context.Context, entries []observe.Entry) error {
	// Encoded outside the lock: marshalling is the slow part and holding a
	// shared writer through it would serialise more than the write.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			// A single entry that will not encode must not cost the batch.
			// It cannot be fixed by retrying, and dropping the rest to spite
			// it would be the wrong lesson.
			continue
		}
	}
	if buf.Len() == 0 {
		return nil
	}

	t.mu.Lock()
	defer t.mu.Unlock()
	if _, err := t.w.Write(buf.Bytes()); err != nil {
		return fmt.Errorf("observe/export: writing entries: %w", err)
	}
	return nil
}

// HTTPOptions configures [HTTP].
type HTTPOptions struct {
	// Endpoint is where batches are posted. Required.
	Endpoint string

	// Token is sent as a bearer credential, when set.
	Token string

	// Source identifies this process in the envelope.
	Source Source

	// Client overrides the HTTP client. The default has a thirty-second
	// timeout, which matters: the zero http.Client has none at all, so a
	// collector that accepts a connection and never answers would hold the
	// exporter's only goroutine for as long as it liked.
	Client *http.Client

	// Uncompressed turns off gzip. Telemetry is repetitive JSON and
	// compresses to a fraction of itself, so the default is on and this is
	// for a collector that cannot decompress.
	Uncompressed bool
}

// envelope is the posted document.
//
// One JSON object rather than newline-delimited entries, unlike [JSONL], and
// for the opposite reason: a file is read by tools that work line by line, a
// collector validates a whole request. An envelope also carries the source
// once, where NDJSON would need it in a header or on every line.
type envelope struct {
	Source  Source          `json:"source"`
	SentAt  time.Time       `json:"sent_at"`
	Entries []observe.Entry `json:"entries"`
}

// HTTP posts batches to a collector.
func HTTP(o HTTPOptions) (Transport, error) {
	if o.Endpoint == "" {
		return nil, fmt.Errorf("observe/export: HTTP needs an endpoint")
	}
	client := o.Client
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &httpTransport{options: o, client: client}, nil
}

type httpTransport struct {
	options HTTPOptions
	client  *http.Client
}

func (t *httpTransport) Send(ctx context.Context, entries []observe.Entry) error {
	body, err := json.Marshal(envelope{
		Source:  t.options.Source,
		SentAt:  time.Now().UTC(),
		Entries: entries,
	})
	if err != nil {
		// Our own encoding, so retrying cannot help.
		return fmt.Errorf("%w: encoding the batch: %v", ErrPermanent, err)
	}

	encoded := body
	if !t.options.Uncompressed {
		var buf bytes.Buffer
		writer := gzip.NewWriter(&buf)
		if _, werr := writer.Write(body); werr != nil {
			return fmt.Errorf("%w: compressing the batch: %v", ErrPermanent, werr)
		}
		if cerr := writer.Close(); cerr != nil {
			return fmt.Errorf("%w: compressing the batch: %v", ErrPermanent, cerr)
		}
		encoded = buf.Bytes()
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.options.Endpoint, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("%w: building the request: %v", ErrPermanent, err)
	}
	request.Header.Set("Content-Type", "application/json")
	if !t.options.Uncompressed {
		request.Header.Set("Content-Encoding", "gzip")
	}
	if t.options.Token != "" {
		request.Header.Set("Authorization", "Bearer "+t.options.Token)
	}

	response, err := t.client.Do(request)
	if err != nil {
		// A transport error is the retryable case: a refused connection, a
		// timeout, a name that did not resolve.
		return fmt.Errorf("observe/export: posting entries: %w", err)
	}
	defer func() {
		// Drained before closing, or the connection cannot be reused and
		// every batch costs a new one.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		_ = response.Body.Close()
	}()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusTooManyRequests,
		response.StatusCode >= 500:
		// The collector is struggling or broken, which is temporary by
		// assumption. Retry, then let the circuit decide.
		return fmt.Errorf("observe/export: the collector answered %d", response.StatusCode)
	default:
		// Any other 4xx is us: a bad token, a wrong path, a body it will not
		// accept. Retrying forever would turn one misconfiguration into a
		// buffer that never drains and a log that never stops.
		return fmt.Errorf("%w: the collector answered %d",
			ErrPermanent, response.StatusCode)
	}
}
