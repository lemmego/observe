package observe

import (
	"log/slog"
	"os"
	"strings"
)

// Profile is a named set of defaults, so the safe configuration is one
// variable rather than five that have to be remembered together.
//
// Development keeps everything: bound parameters, statements as written,
// every log line. That is the right trade for a machine with one user, and
// it is what this package was built for.
//
// Production trades detail for volume, because always-on changes which
// mistakes are expensive. The things it turns off are the ones that cost
// most per entry and are worth least in aggregate.
type Profile string

const (
	ProfileDevelopment Profile = "development"
	ProfileProduction  Profile = "production"
)

// EnvProfile is the variable a profile is read from.
const EnvProfile = "OBSERVE_PROFILE"

// ProfileFromEnv reads OBSERVE_PROFILE, defaulting to development.
//
// An unrecognised value is development rather than an error, and that
// direction is deliberate: the failure of guessing wrong here is either
// "recorded more than you meant" or "recorded less than you meant", and the
// first is the one you can see. A production deployment that silently
// recorded nothing because of a typo would look like a broken installation.
//
// Read directly rather than through config.MustEnv, for the reason
// OBSERVE_ENABLED is: MustEnv treats a variable that is present and empty as
// a decision, and panics on a value it cannot parse.
func ProfileFromEnv() Profile {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvProfile))) {
	case string(ProfileProduction), "prod":
		return ProfileProduction
	default:
		return ProfileDevelopment
	}
}

// Options returns the recorder options this profile implies.
func (p Profile) Options() []Option {
	if p != ProfileProduction {
		return nil
	}
	return []Option{WithoutArgs(), WithNormalisedStatements()}
}

// MinLogLevel is the level below which log records are not recorded.
//
// Debug in development, which is to say no floor at all. Info in production:
// a debug line is written for somebody reading a log while they work, and
// recording every one of them into a store is the single easiest way to turn
// a telemetry pipeline into the application's largest writer.
func (p Profile) MinLogLevel() slog.Level {
	if p == ProfileProduction {
		return slog.LevelInfo
	}
	return slog.LevelDebug
}

// SampleRate is the rate used when OBSERVE_SAMPLE says nothing.
//
// Everything in development. A tenth in production — chosen rather than
// derived, and the point is that it is *not* one: a profile that defaulted
// to recording every request would make the word "production" mean nothing,
// and somebody who wants everything can say so.
//
// Failures are kept whatever this says. See the recorder's bypassesSampling.
func (p Profile) SampleRate() float64 {
	if p == ProfileProduction {
		return 0.1
	}
	return 1
}

// String makes a profile printable in a log line.
func (p Profile) String() string { return string(p) }
