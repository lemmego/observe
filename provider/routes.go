package provider

import (
	"log/slog"
	"net/http"

	"github.com/lemmego/api/app"
	"github.com/lemmego/observe/web"
)

// AddRoutes mounts the dashboard.
//
// As a raw http.Handler on the mux, because the dashboard owns a subtree
// rather than a route. The cost is that none of the router's app.Handler
// middleware runs for it — so authentication is done here, by hand — and that
// these paths do not appear in Router().Routes(), which is why `list-routes`
// will not show them.
func (p *Provider) AddRoutes() app.RouteCallback {
	return func(a app.App) {
		recorder := p.Recorder()
		if recorder == nil {
			// Recording is off, so there is nothing to look at. Mounting an
			// empty dashboard would be worse than mounting none: it would
			// read as "this application does nothing".
			return
		}

		server := web.New(recorder, p.prefix())
		if p.DashboardAuth != nil {
			server.UseAuth(p.guard(a))
		}

		// Trailing slash: the dashboard is a subtree, and without it the mux
		// matches only the exact prefix.
		a.Router().Handle(p.prefix()+"/", server)
		slog.Debug("observe: dashboard mounted", "prefix", p.prefix(),
			"open", p.DashboardAuth != nil)
	}
}

// guard bridges an app.Context predicate onto raw http middleware.
//
// A handler mounted with Router().Handle sits on the bare ServeMux, so
// nothing has built a Context for it. app.NewContext is the sanctioned way
// to make one, and the rebound request is what goes downstream — passing the
// original r would discard anything the predicate put in the context, such as
// the user it resolved.
func (p *Provider) guard(a app.App) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := app.NewContext(a, w, r)
			if !p.DashboardAuth(c) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.WriteHeader(http.StatusForbidden)
				//nolint:errcheck // a client that hung up is not worth reporting.
				_, _ = w.Write([]byte(`{"error":"observe: not permitted"}`))
				return
			}
			next.ServeHTTP(w, c.Request())
		})
	}
}
