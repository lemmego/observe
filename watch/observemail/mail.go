// Package observemail records mail as it is sent.
//
// A decorator rather than a replacement, which is the distinction that
// matters: mailtest's recorder substitutes for the transport and delivers
// nothing, so it cannot be used to watch a real one. This wraps.
//
//	&mail.Provider{Transport: observemail.Watch(realTransport, recorder)}
//
// Queued mail is sent by the worker process, not the web process, so a
// message dispatched by a request is recorded under the job that sent it —
// and if no worker is running, it is not recorded at all, because it has not
// been sent. "No mail entries" therefore has two causes, and one of them is
// "no worker".
package observemail

import (
	"encoding/json"
	"log/slog"
	"time"

	"github.com/lemmego/mail"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "mail"

// Watch returns a transport that records what it sends and then delegates.
func Watch(transport mail.Transport, recorder *observe.Recorder) mail.Transport {
	if transport == nil || recorder == nil {
		return transport
	}
	recorder.Watching(Watcher)
	return &watching{wrapped: transport, recorder: recorder}
}

type watching struct {
	wrapped  mail.Transport
	recorder *observe.Recorder
}

var _ mail.Transport = (*watching)(nil)

func (w *watching) Send(ctx mail.Context, message *mail.Message) error {
	started := time.Now()
	err := w.wrapped.Send(ctx, message)

	entry := observe.Entry{
		Kind:     observe.KindMail,
		At:       started,
		Duration: time.Since(started),
		Level:    slog.LevelInfo,
		Summary:  "mail " + subjectOf(message),
		Watcher:  Watcher,
	}
	if data, jerr := json.Marshal(describe(message)); jerr == nil {
		entry.Data = data
	}
	if err != nil {
		entry.Level = slog.LevelError
		entry.Err = err.Error()
	}

	w.recorder.Record(ctx, entry)
	return err
}

type mailPayload struct {
	Subject string   `json:"subject,omitempty"`
	To      []string `json:"to,omitempty"`
	From    string   `json:"from,omitempty"`
}

// describe records the envelope and not the body.
//
// A rendered body is the one place a password-reset link lives, and a link is
// a credential: anybody who can read this table could use it. The subject and
// the recipients are what you need to answer "was it sent, and to whom".
func describe(message *mail.Message) mailPayload {
	if message == nil {
		return mailPayload{}
	}
	out := mailPayload{Subject: message.Subject, From: message.From.Email}
	for _, to := range message.To {
		out.To = append(out.To, to.Email)
	}
	return out
}

func subjectOf(message *mail.Message) string {
	if message == nil || message.Subject == "" {
		return "(no subject)"
	}
	return message.Subject
}
