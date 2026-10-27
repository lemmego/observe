# lemmego/observe

Records what your application did while answering a request — and, underneath
that request, every query, log line, cache operation, job and mail it caused,
in order, with timings.

Reach for it when a page is slow or wrong and the logs cannot tell you why.
Logs are a flat stream with no notion of which request a line belongs to; this
is the question they cannot answer.

```go
import "github.com/lemmego/observe"
```

```
$ lemmego run observe:last
  request   412.80ms  GET /feed
  query       3.10ms  SELECT * FROM posts WHERE circle_id IS NULL ORDER BY id DESC LIMIT 25
  query       1.90ms  SELECT * FROM users WHERE id = $1
  query       1.80ms  SELECT * FROM users WHERE id = $1
  …
  log         0.00ms  the avatar renderer fell back to the default

N+1: 25 executions of SELECT * FROM users WHERE id = ?  (47ms total)
```

## Install

```bash
go get github.com/lemmego/observe
```

Then publish the migration, which creates the one table entries live in:

```bash
lemmego run publish --tags=observe-migrations
go build ./...          # the published migration must be compiled in
lemmego run migrate up
```

Step 2 is the easy one to miss: the migration was written by a running binary
that does not contain it yet.

## Register it

```go
// bootstrap/providers.go
import (
    observeprovider "github.com/lemmego/observe/provider"
    "github.com/lemmego/observe/watch/observeslog"
)

func LoadProviders() []app.Provider {
    return []app.Provider{
        &logger.Provider{Options: log.Options{
            // Log lines land under the request that produced them.
            Extra: []slog.Handler{observeslog.Handler()},
        }},
        &ormconnector.Provider{},

        // After the connector, whose connection it borrows, and before
        // anything whose queries you want recorded.
        &observeprovider.Provider{
            DashboardAuth: func(c app.Context) bool {
                user, ok := auth.UserAs[*models.User](c)
                return ok && user.IsStaff
            },
        },
    }
}
```

Then turn it on:

```bash
OBSERVE_ENABLED=true
```

**It is off unless you say otherwise**, because the entries hold request
headers, bound query parameters and every statement you ran.

## Record your queries

Queries are collected **below** your ORM, at the `database/sql` driver layer.
One wrapper covers `lemmego/orm`, GORM, Bun, `sqlx`, raw `database/sql`,
`lemmego/migration`, `tasker` and your session store — so this works whatever
you chose, including nothing.

```go
import (
    "database/sql"
    "github.com/lemmego/observe"
    "github.com/lib/pq"
)

func init() {
    sql.Register("postgres-observed", observe.Wrap(&pq.Driver{}))
}
```

```bash
DB_DRIVER=postgres-observed
```

That is the whole wiring, and it needs no change to the framework: the three
places that open a pool all take the driver name from your configuration.

Two paths are not covered this way. A GORM dialector builds its own pool, so
hand it an instrumented `*sql.DB` instead; and `sql.OpenDB(connector)`
bypasses the driver-name registry, so wrap the connector with
`observe.WrapConnector`.

### If you are not on `database/sql` at all

```go
started := time.Now()
cursor, err := collection.Find(ctx, filter)
observe.RecordQuery(ctx, "mongo", "users.find", args, time.Since(started), err)
```

One line, and the entry is treated identically by the dashboard and the MCP
tools. There is no second-class tier for a store this package has not heard
of — which is the point, since the framework is database-agnostic and so this
has to be.

## Read it three ways

**A dashboard**, at `/observe`. Server-rendered Go, no build step. Click a
request to see everything it did.

**A terminal.** `lemmego run observe:last`, or
`lemmego run observe:last --request <id>`.

**An AI agent**, over MCP — which is the half a dashboard cannot be:

```go
&mcp.Provider{Options: []mcp.Option{
    mcp.WithTools(observemcp.Tools()),
}},
```

It takes no recorder and the provider order does not matter: the tools resolve
the recorder when a call arrives, because `LoadProviders` is written before
anything exists.

| Tool | Answers |
|---|---|
| `observe-requests` | recent requests, with timings |
| `observe-request` | one request, in full, in order |
| `observe-slow-queries` | the slowest statements |
| `observe-repeated-queries` | N+1 candidates, by request |
| `observe-errors` | error-level entries of every kind, structured |

These supersede `read-log-entries` and `last-error`, which tail a rotating
file and match with `strings.Contains` — so `last-error` finds a request to
`/error-pages`. Keep those as the raw-file escape hatch.

## N+1 detection

Statements are grouped with their literals stripped, so a lookup repeated with
fifty different ids counts as fifty executions of one shape. Grouping happens
when you read, not when you record, so a request pays nothing for it and you
can change the threshold without re-recording anything.

## Three things it guarantees

**Recording never blocks a request.** The driver wrapper runs inside every
statement, so a store that stops answering costs dropped entries and never
latency. Drops are counted, reported through the dashboard, and logged once —
never silent. If you see them, your store is not keeping up.

**Recording never recurses.** The store writes through the same database the
wrapper watches. Two independent guards stop the loop, and either alone is
sufficient; see the tests before removing one as redundant.

**Nothing unbounded is keyed on anything a client controls.** The memory store
is a bounded LRU, not a map.

## What it does not record

No request bodies, no response bodies, no cached values, no job payloads, no
mail bodies. Each is where a credential actually lives — a cached value is a
rendered page with somebody's session in it, and a mail body holds a
password-reset link, which *is* a credential.

Headers and bound query parameters **are** recorded, and go through redaction
first: secret-looking keys are withheld, and a credential inside a value is
rewritten, so `DATABASE_URL` keeps its host and loses its password.

## Choosing a store

| Store | Use it when |
|---|---|
| `store/sqlstore` | **almost always** — every process can read it |
| `store/memory` | tests, and a single process with no database |

The memory store answers the wrong process. `lemmego run mcp:serve` boots a
second one and a worker is a third, so entries recorded by your web process
are not in the memory of the process your agent is asking — and the answer
reads as "this application makes no queries" rather than "you are looking
somewhere else". The provider warns when it falls back to it.

## Limits worth knowing before you go looking

**Cache operations are not correlated to a request**, and cannot be:
`cache.Listener` is `func(Event)` with no `context.Context`, so there is
nowhere to read a request id from. Hit and miss rates are legible; "which
lookups did this request make" is not.

**A job correlates to itself, not to the request that queued it.**
`tasker.Manager.Dispatch` has no hook, so enqueueing is not observable at all,
and a job runs in another process anyway.

**A driver offering only the legacy non-context interfaces** records its
statements uncorrelated rather than dropping them, and the entry says
`"correlation":"unavailable"` so you stop looking for a cause.

**Nothing prunes itself.** Run `lemmego run observe:prune` on a schedule.

## Safety

- Off unless `OBSERVE_ENABLED` says otherwise.
- `DashboardAuth` nil means **no access**, not no authentication — the
  dashboard answers 401 to everything until you give it a predicate.
- The dashboard is mounted on the bare mux, so none of your router's
  middleware runs for it. The guard is applied by this package, by hand.
- Set a `Sampler` before using this under real traffic.

## Testing

```bash
go test ./...
GOWORK=off go test -race ./...   # as a published module
```

`storetest` is exported, so a store of your own can prove itself against the
same contract both built-in stores pass:

```go
func TestMyStoreSatisfiesTheContract(t *testing.T) {
    storetest.Run(t, func(*testing.T) observe.Store { return NewMyStore() })
}
```

## License

[MIT](https://choosealicense.com/licenses/mit/)
