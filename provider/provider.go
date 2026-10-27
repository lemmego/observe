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
	"github.com/lemmego/api/db"
	"github.com/lemmego/observe"
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

	// Sampler decides which entries are kept. Nil keeps everything, which is
	// right in development and wants reconsidering in production.
	Sampler func(observe.Entry) bool

	// Retention is how long entries are kept by the prune command. Zero means
	// seven days.
	Retention time.Duration

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

	options := p.Options
	if p.Sampler != nil {
		options = append(options, observe.WithSampler(p.Sampler))
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
