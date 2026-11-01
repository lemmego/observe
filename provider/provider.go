// Package provider wires observe into a Lemmego application.
//
// Everything needing the framework lives here rather than in the root, which
// is what keeps observe.Wrap and observe.RecordQuery usable from a program
// that is not a Lemmego application — and keeps a consumer of the root from
// inheriting api/app's dependency tree. observe/deps_test.go measures that
// rather than trusting it.
package provider

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lemmego/api/app"
	"github.com/lemmego/api/config"
	"github.com/lemmego/api/db"
	"github.com/lemmego/observe"
	"github.com/lemmego/observe/export"
	"github.com/lemmego/observe/store/memory"
	"github.com/lemmego/observe/store/sqlstore"
)

// Provider installs the recorder, the request middleware and the dashboard.
//
// It is off unless enabled. Entries hold request headers, bound query
// parameters and SQL, so an application that has not asked for this should
// not be keeping any of it.
type Provider struct {
	// Enabled turns recording on. When nil, OBSERVE_ENABLED decides, and its
	// absence means off.
	//
	// A pointer so "not configured" and "configured false" stay distinct: a
	// deployment that sets the variable to false has made a decision, and a
	// field that cannot express it would silently let the environment win.
	Enabled *bool

	// Store overrides where entries go. Nil resolves the application's
	// database, falling back to an in-memory store when there is none.
	Store observe.Store

	// DashboardAuth decides who may read the dashboard.
	//
	// Leaving it nil is not "no authentication", it is "no access". That is
	// deliberate, and it matters more here than it does for a queue
	// dashboard: these entries hold request headers, bound parameters and
	// every statement the application ran.
	DashboardAuth func(c app.Context) bool

	// RoutePrefix is where the dashboard mounts. Empty means /observe.
	RoutePrefix string

	// Sampler decides which requests are kept, from the correlation id
	// alone.
	//
	// An id rather than an Entry, because the decision is made before an
	// Entry exists — that is what makes sampling cheap rather than merely
	// correct. For a finer filter over built entries, pass
	// observe.WithSampler through Options.
	//
	// Nil defers to OBSERVE_SAMPLE, a rate between 0 and 1 read as a
	// fraction of requests kept whole — see observe.SampleRequests. An
	// absent or unparsable variable keeps everything, which is right in
	// development and wants reconsidering in production.
	Sampler func(requestID string) bool

	// Retention is how long entries are kept by the prune command. Zero means
	// seven days.
	Retention time.Duration

	// Profile selects a preset. Empty defers to OBSERVE_PROFILE, whose
	// absence means development.
	//
	// Production stops recording bound parameters, records statements as
	// shapes, floors the log level at info and samples a tenth of requests —
	// one variable rather than five that have to be remembered together.
	// Anything set explicitly here or in Options overrides it.
	Profile observe.Profile

	// Source identifies this process in recorded entries and in anything
	// shipped to a collector.
	//
	// Host and Instance are filled in automatically. Service defaults to
	// APP_NAME, Environment to the profile, and Release to OBSERVE_RELEASE —
	// which is the one worth setting in a deployment, because it is what
	// makes "did the new build do this" answerable.
	Source observe.Source

	// Sinks send every flushed batch somewhere beyond the store — a
	// collector, a file, anything satisfying observe.Sink.
	//
	// Nil defers to OBSERVE_EXPORT_URL, which wires observe/export's HTTP
	// transport with OBSERVE_EXPORT_TOKEN as its credential. One variable
	// again, for the same reason the profile is one.
	//
	// A sink implementing observe.SinkCloser is closed with the recorder, so
	// an exporter flushes what it is holding on a graceful shutdown.
	Sinks []observe.Sink

	// Options are passed to the recorder.
	Options []observe.Option

	mu       sync.RWMutex
	recorder *observe.Recorder
	shutdown sync.Once
}

const (
	// DefaultRoutePrefix is where the dashboard mounts.
	DefaultRoutePrefix = "/observe"

	// DefaultRetention is how long entries are kept.
	DefaultRetention = 7 * 24 * time.Hour
)

var (
	_ app.Provider            = (*Provider)(nil)
	_ app.RouteProvider       = (*Provider)(nil)
	_ app.CommandProvider     = (*Provider)(nil)
	_ app.PublishableProvider = (*Provider)(nil)
	_ app.ShutdownProvider    = (*Provider)(nil)
)

// Provide builds the recorder and installs the request middleware.
//
// List it early in LoadProviders — after the database connector, whose
// connection it borrows, and before anything whose queries you want
// recorded. It does not create its tables; see AddPublishables.
func (p *Provider) Provide(a app.App) error {
	if !p.enabled(a) {
		slog.Debug("observe: recording is off; set OBSERVE_ENABLED=true to turn it on")
		return nil
	}

	store, err := p.resolveStore(a)
	if err != nil {
		return err
	}

	profile := p.profile()

	// The profile first, so an explicit Option or Sampler overrides it —
	// New applies options in order, and a field set in code is a decision
	// somebody wrote down.
	options := append([]observe.Option{observe.WithProfile(profile)}, p.Options...)
	if sampler := p.sampler(profile); sampler != nil {
		options = append(options, observe.WithRequestSampler(sampler))
	}

	// Said out loud, once, because a profile that quietly stopped recording
	// bound parameters and nine requests in ten would look like a broken
	// installation to the next person who opened the dashboard.
	if profile == observe.ProfileProduction {
		slog.Info("observe: the production profile is on",
			"bound_parameters", "not recorded",
			"statements", "normalised",
			"log_floor", profile.MinLogLevel(),
			"sample_rate", p.sampleRate(profile))
	}

	options = append(options, observe.WithSource(p.source(profile)))

	sinks, err := p.sinks(a, profile)
	if err != nil {
		return err
	}
	if len(sinks) > 0 {
		options = append(options, observe.WithSinks(sinks...))
	}

	recorder := observe.New(store, options...)
	recorder.Watching(WatcherHTTP)

	p.mu.Lock()
	p.recorder = recorder
	p.mu.Unlock()

	// The default is what observe.Wrap and observe.RecordQuery find. They run
	// from inside a database driver and from application code that has no
	// route to the container, so there is nothing to hand a recorder to.
	observe.SetDefault(recorder)

	a.AddService(recorder)

	// Installed on the router rather than returned from AddMiddlewares,
	// because the router's own middleware runs after pattern matching — so a
	// request that matched no route would go unrecorded, and that is a
	// request somebody is often trying to explain.
	a.Router().Use(Middleware(recorder))

	if p.DashboardAuth == nil {
		slog.Warn("observe: the dashboard is mounted but closed; set " +
			"observe/provider.Provider.DashboardAuth to open it. Entries hold " +
			"request headers, bound parameters and SQL, so leaving it nil " +
			"means no access rather than no authentication.")
	}
	return nil
}

// sampler resolves which entries are kept.
//
// An explicit Sampler wins over the environment: a field set in code is a
// decision somebody wrote down, and letting a stray variable override it
// would make the code lie.
//
// OBSERVE_SAMPLE is read directly rather than through config.MustEnv for the
// same reason OBSERVE_ENABLED is: MustEnv treats a variable that is present
// and empty as a decision, and panics on a value it cannot parse. A stray
// `OBSERVE_SAMPLE=` would otherwise mean something different from an absent
// one, and `OBSERVE_SAMPLE=half` would take the process down rather than
// record everything and complain.
func (p *Provider) sampler(profile observe.Profile) func(requestID string) bool {
	if p.Sampler != nil {
		return p.Sampler
	}

	rate := p.sampleRate(profile)
	if rate >= 1 {
		return nil
	}
	return observe.SampleRequests(rate)
}

// sampleRate resolves the rate: OBSERVE_SAMPLE if it says anything usable,
// otherwise the profile's default.
//
// The profile having a default is the point of it — production means a tenth
// unless somebody says otherwise, rather than meaning a tenth only if they
// remembered a second variable.
func (p *Provider) sampleRate(profile observe.Profile) float64 {
	raw := strings.TrimSpace(os.Getenv("OBSERVE_SAMPLE"))
	if raw == "" {
		return profile.SampleRate()
	}
	rate, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		slog.Warn("observe: OBSERVE_SAMPLE is not a number, so the profile's "+
			"rate is used", "value", raw, "profile", profile, "rate", profile.SampleRate())
		return profile.SampleRate()
	}
	if rate < 0 {
		slog.Warn("observe: OBSERVE_SAMPLE is negative, so nothing will be "+
			"recorded; set it between 0 and 1", "value", raw)
	}
	return rate
}

// sinks resolves where entries are shipped, beyond the store.
//
// An explicit Sinks field wins over the environment, for the reason an
// explicit Sampler does: a field set in code is a decision somebody wrote
// down, and letting a stray variable override it would make the code lie.
//
// A configured endpoint that will not build is a boot error rather than a
// warning. An exporter is not an optional nicety once somebody has gone to
// the trouble of configuring one, and the alternative is an application that
// starts, looks healthy and ships nothing.
func (p *Provider) sinks(a app.App, profile observe.Profile) ([]observe.Sink, error) {
	if len(p.Sinks) > 0 {
		return p.Sinks, nil
	}

	endpoint := strings.TrimSpace(os.Getenv("OBSERVE_EXPORT_URL"))
	if endpoint == "" {
		return nil, nil
	}

	transport, err := export.HTTP(export.HTTPOptions{
		Endpoint: endpoint,
		Token:    strings.TrimSpace(os.Getenv("OBSERVE_EXPORT_TOKEN")),
		Source:   p.source(profile),
	})
	if err != nil {
		return nil, fmt.Errorf("observe: OBSERVE_EXPORT_URL: %w", err)
	}

	slog.Info("observe: shipping entries to a collector",
		"endpoint", endpoint, "authenticated", os.Getenv("OBSERVE_EXPORT_TOKEN") != "")

	return []observe.Sink{export.New(transport)}, nil
}

// source resolves this process's identity, with explicit fields winning.
//
// Nothing here can guess a release, so it is read from the environment and
// left empty when absent — an entry labelled with the wrong release is an
// entry that sends somebody to the wrong commit.
func (p *Provider) source(profile observe.Profile) observe.Source {
	resolved := p.Source
	if resolved.Service == "" {
		resolved.Service = config.MustEnv("APP_NAME", "")
	}
	if resolved.Environment == "" {
		resolved.Environment = string(profile)
	}
	if resolved.Release == "" {
		resolved.Release = strings.TrimSpace(os.Getenv("OBSERVE_RELEASE"))
	}
	return resolved
}

// profile resolves OBSERVE_PROFILE, with an explicit field winning.
func (p *Provider) profile() observe.Profile {
	if p.Profile != "" {
		return p.Profile
	}
	return observe.ProfileFromEnv()
}

// Recorder returns the installed recorder, or nil when recording is off.
func (p *Provider) Recorder() *observe.Recorder {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.recorder
}

// enabled reports whether recording is on.
//
// The variable is read directly rather than through config.MustEnv, which
// treats a variable that is present and empty as a decision and panics on a
// value it cannot parse. A stray `OBSERVE_ENABLED=` line would otherwise mean
// something different from an absent one, and a typo would take the process
// down before anything could log why.
func (p *Provider) enabled(a app.App) bool {
	if p.Enabled != nil {
		return *p.Enabled
	}

	raw := strings.TrimSpace(os.Getenv("OBSERVE_ENABLED"))
	if raw == "" {
		return false
	}
	on, err := strconv.ParseBool(raw)
	if err != nil {
		slog.Warn("observe: OBSERVE_ENABLED could not be read as a boolean, so "+
			"recording stays off", "value", raw)
		return false
	}
	if on && a.InProduction() && p.DashboardAuth == nil {
		slog.Warn("observe: recording is on in production with no DashboardAuth, " +
			"so the dashboard will refuse every request")
	}
	return on
}

// resolveStore picks where entries go.
func (p *Provider) resolveStore(a app.App) (observe.Store, error) {
	if p.Store != nil {
		return p.Store, nil
	}

	conn, ok := db.Resolve(a)
	if !ok {
		// A memory store rather than a refusal, because an application
		// without a database is a legitimate shape — but it is only right in
		// one process, so say so rather than letting somebody discover it by
		// asking an agent and getting nothing.
		slog.Warn("observe: no database connection, so entries are kept in " +
			"memory and only this process can read them. `lemmego run " +
			"mcp:serve` is a different process and will see none of them.")
		return memory.New(), nil
	}

	dialect := sqlstore.Dialect(conn.Dialect())
	store, err := sqlstore.New(conn.SQLDB(), dialect)
	if err != nil {
		return nil, fmt.Errorf("observe: %w", err)
	}
	return store, nil
}

// Shutdown drains what is queued.
//
// Idempotent, because a ShutdownProvider may be called more than once and
// closing a closed recorder twice must not panic.
func (p *Provider) Shutdown(ctx context.Context) error {
	var err error
	p.shutdown.Do(func() {
		recorder := p.Recorder()
		if recorder == nil {
			return
		}
		err = recorder.Close(ctx)

		// Only clear the default if it is still ours: a second provider may
		// have replaced it.
		if observe.Default() == recorder {
			observe.SetDefault(nil)
		}
	})
	return err
}

func (p *Provider) prefix() string {
	if p.RoutePrefix == "" {
		return DefaultRoutePrefix
	}
	return "/" + strings.Trim(p.RoutePrefix, "/")
}

func (p *Provider) retention() time.Duration {
	if p.Retention <= 0 {
		return DefaultRetention
	}
	return p.Retention
}

var errRecordingOff = errors.New("observe: recording is off")
