// Package observeauthz records authorization decisions.
//
// Both outcomes, not just refusals. A panel showing only denials cannot show
// the allow nobody intended, and the two failures are symmetrical: a rule
// that is too tight produces a 403 somebody complains about, and a rule that
// is too loose produces nothing at all until it matters.
//
// It also sees the denials authz reaches without consulting a rule — an
// unregistered ability, a gate called with a resource, an anonymous request.
// Those are the ones worth having: authz fails closed, so a typo in an
// ability name is an ordinary-looking 403, and this is what stops it looking
// ordinary.
//
// Install it on the registry, which the authz provider owns:
//
//	observeauthz.Watch(authz.FromApp(a), recorder)
//
// Needs authz v0.3.0 or later, where Registry.Observe landed. Registration is
// additive, so this does not displace an application's own audit log.
//
// # What it does not record
//
// The subject and the resource are the application's own values — a user
// struct holds an email address and a password hash, and a resource can hold
// anything at all. Only their types are recorded, plus the subject's id when
// it can be had from a well-known interface. Who was refused what is legible;
// the contents of either value are not.
package observeauthz

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/lemmego/authz"
	"github.com/lemmego/observe"
)

// Watcher is the name entries from this package carry.
const Watcher = "authz"

// identifier is the one thing worth pulling off a subject: which user it was.
//
// auth's own provider interface declares GetID, so this matches without
// importing auth — which would be a module dependency for one method on a
// value this package already has.
type identifier interface{ GetID() string }

type gatePayload struct {
	Ability string `json:"ability"`
	Allowed bool   `json:"allowed"`

	// Reason is authz's own prose, which is the difference between a useful
	// entry and the word "denied".
	Reason string `json:"reason,omitempty"`

	// SubjectType and SubjectID say who asked, by type and id only. Never
	// the value: a user struct holds an email address and a password hash.
	SubjectType string `json:"subject_type,omitempty"`
	SubjectID   string `json:"subject_id,omitempty"`

	// ResourceType is what was asked about. Not the resource, which can hold
	// anything the application put in it.
	ResourceType string `json:"resource_type,omitempty"`

	// Anonymous distinguishes "nobody was signed in" from "a subject was
	// present and this watcher could not name it", which otherwise look the
	// same in a list of blank subjects.
	Anonymous bool `json:"anonymous,omitempty"`
}

// Watch records every decision the registry makes onto the recorder.
func Watch(registry *authz.Registry, recorder *observe.Recorder) {
	if registry == nil || recorder == nil {
		return
	}

	registry.Observe(func(ctx context.Context, o authz.Observation) {
		payload := gatePayload{
			Ability: o.Ability,
			Allowed: o.Decision.Allowed,
			Reason:  o.Decision.Reason,
		}
		if o.Subject == nil {
			payload.Anonymous = true
		} else {
			payload.SubjectType = fmt.Sprintf("%T", o.Subject)
			if named, ok := o.Subject.(identifier); ok {
				payload.SubjectID = named.GetID()
			}
		}
		if o.HasResource && o.Resource != nil {
			payload.ResourceType = fmt.Sprintf("%T", o.Resource)
		}

		verb := "allowed"
		level := slog.LevelInfo
		if !o.Decision.Allowed {
			verb = "denied"
			// Warn rather than error: a refusal is the system working, and
			// an error level would make the panel's own errors view useless
			// on any application that refuses anything.
			level = slog.LevelWarn
		}

		entry := observe.Entry{
			Kind:    observe.KindGate,
			Level:   level,
			Summary: verb + " " + o.Ability,
			Watcher: Watcher,
		}
		if data, err := json.Marshal(payload); err == nil {
			entry.Data = data
		}

		recorder.Record(ctx, entry)
	})

	recorder.Watching(Watcher)
}
