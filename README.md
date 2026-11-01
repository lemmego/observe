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

Three files are published, not one: the table, and an `ALTER` for each column
added since — `tags` and `source`. A new migration rather than an edit to the
first, because the first has already run everywhere it was going to, so
editing it would change nothing for an existing project and leave its table a
column short of what the store writes. **Upgrading from any earlier version
means republishing and migrating again**; a republish only writes the files
that are missing.

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
                // auth.Check first, and this is not optional. The dashboard
                // mounts with Router().Handle, on the bare ServeMux, so no
                // route middleware has run — and auth.UserAs is a context
                // read, not a lookup. Without this it answers false for
                // everybody, however staff they are.
                _ = auth.Check(c)
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

## The watchers

Requests and queries come from the provider and the driver wrapper. The rest
attach to something the application already has, and each is one line — but
each is a line somebody has to write, so a watcher you do not wire records
nothing and says nothing about it. `observe:last` prints which are active.

| Watcher | Wire it | Needs |
|---|---|---|
| `http` | automatic, from the provider | |
| `sqldriver` | `observe.Wrap(driver)`, below | |
| `exception` | `observehttp.Recover()` **in place of** your recoverer | |
| `slog` | `observeslog.Handler()` in `log.Options.Extra` | |
| `cache` | `observecache.Watch(cache, observe.Default())` | the cache |
| `job` | `observejob.Watch(observe.Default())` | |
| `mail` | `Transport: observemail.Watch(transport, observe.Default())` | the transport |
| `notification` | `EventSink: observenotify.Sink()` on notify's provider | notify v0.3.0 |
| `httpclient` | `&http.Client{Transport: observe.WrapTransport(nil)}` | |
| `dump` | `observe.Dump(ctx, "label", value)` where you want it | |
| `authz` | `observeauthz.Watch(registry, observe.Default())` | authz v0.3.0 |
| `view` | `observeview.Watch(observe.Default())` | api v0.10.0 |
| `event` | `observeevents.Watch(a, observe.Default(), "launch.published", …)` | api v0.10.0 |
| `schedule` | `observeschedule.Watch(sched, observe.Default())` | tasker v0.2.0 |

`observe.Default()` is nil until the provider has run, and `LoadProviders`
builds its whole slice before any provider runs — so a watcher constructed
inside that slice gets nil and silently records nothing. Attach them from a
provider of your own, registered after the things they watch.

### Exceptions

`observehttp.Recover()` **replaces** the recovering middleware you already
have rather than joining it. Two recoverers is not twice the safety: a panic
unwinds outward, so the one nearest the handler catches it and the other
never fires. This one has to be the nearest, or it records nothing —
replacing yours rather than adding to it is the only arrangement that
guarantees that.

```go
// bootstrap/http_middleware.go
func LoadHTTPMiddlewares() []app.HTTPMiddleware {
    return []app.HTTPMiddleware{
        observehttp.Recover(), // was middleware.Recoverer()
        middleware.RequestLogger(),
    }
}
```

What you get back is the stack. `api/middleware.Recoverer` delegates to chi's,
which prints the stack to stderr through its own pretty-printer — not through
slog, so no log watcher sees it, and not into any store, so by the time
somebody asks what happened the only copy has scrolled. observe could see the
*shape* of the failure, a request entry at 500, and nothing about its cause.

It answers 500 the same way chi's does, re-panics `http.ErrAbortHandler`
rather than reporting a cancelled response as a crash, and still logs at
error level — the store is for looking things up afterwards, not a reason to
go quiet during development.

The exception entry carries the request id, so it groups under the request
that caused it.

### Outgoing HTTP, and dumps

Two of these are not watchers you wire once. They live in the root package
beside `Wrap` and `RecordQuery`, standard library only, so a program
instruments itself without importing anything of the framework's.

```go
client := &http.Client{Transport: observe.WrapTransport(nil)}

observe.Dump(ctx, "why is this nil", order)
```

`WrapTransport` is how a slow third party stops looking like a slow
application. It records method, host, path, status and duration — and **not**
the query string, which is where an API key most often is. A redactor only
withholds what it recognises, and `?access_token=` arrives under too many
names, so the query goes entirely and the summary ends `?…` to say there was
one. No bodies and no headers either; an outgoing request's headers are
nearly all Authorization.

`Dump` is this project's `dd()`, and the one kind produced on purpose. It
records **exactly** what you give it — no redaction, because a debugger that
hid the thing you asked to see would be useless — so a dump that reaches
production is a mistake the dashboard will happily show you. That is the
trade, and it is why the kind has its own filter: find them before you ship.

### Notifications

The notification watcher attaches to notify's event sink rather than
decorating the channels, and the reason is worth knowing before you reach
for a wrapper of your own. A `Channel` may also implement `BulkChannel`,
`Supports`, `provider.Publisher` or `io.Closer`, and notify type-asserts for
all four — so a decorator would have to reproduce every combination of them
or silently switch the features off.

Entries carry the request id, so "what did this request tell people" is
answerable — which needs notify v0.3.0, where `notify.Listener` gained a
context. A notification sent from a job or a command has no request behind
it, and the entry says `correlation: unavailable` rather than leaving the
field blank, so a reader stops looking for a cause that was never recorded.

Never the rendered body. A notification's body is the message somebody
received, and it holds whatever the template put in it.

### Authorization decisions

```go
observeauthz.Watch(authz.FromApp(a), observe.Default())
```

Both outcomes, not just refusals. A panel showing only denials cannot show
the allow nobody intended, and the two failures are symmetrical: a rule that
is too tight produces a complaint, a rule that is too loose produces nothing
at all until it matters.

It also sees the denials `authz` reaches **without consulting a rule** — an
unregistered ability, a gate called with a resource, an anonymous request.
Those are the ones worth having: `authz` fails closed, so a typo in an
ability name is an ordinary-looking 403, and this is what stops it looking
ordinary.

The subject and the resource are your types, so only their types are
recorded, plus the subject's id. A user struct holds an email address and a
password hash; who was refused what is legible, the contents of either value
are not. A refusal records at **warn**, not error — a refusal is the system
working, and error level would make the errors view useless on any
application that refuses anything.

### Views

```go
observeview.Watch(observe.Default())
```

One question, and it is the only one a query entry cannot answer: why is this
page slow when the queries are fast. A template looping over an
already-loaded relation issues no statements at all, so before this it looked
identical to a fast page.

The view's name comes from `app.ViewNamer`, which `res.Template` and
`inertia.InertiaResponse` implement — so you get `Profile/Show`, not
`*inertia.InertiaResponse`. Both render paths are covered: `ctx.Render` and
`ctx.Templ`, which takes a component directly and never reaches `Render`.

No props and no output. An Inertia page's props **are** the response body.

### Application events

```go
observeevents.Watch(a, observe.Default(), "launch.published", "comment.created")
```

`app.Dispatch` is in-process and synchronous, so an event leaves nothing
behind: whether it fired at all is not recoverable afterwards, and a feature
whose cross-cutting reaction never happens looks identical to one whose
listener is broken.

You have to name the events. The emitter's only registration takes a name, so
there is no "every event" to offer, and covering some of them silently would
be worse than asking. Not the payload either — an application's payloads are
its own domain objects, and a `*models.User` is a plausible one.

### Scheduled runs

```go
observeschedule.Watch(sched, observe.Default())
```

Separate from the job watcher because the queue says what work exists and the
schedule says whether 04:20 came round. And a run that **failed to enqueue**
leaves no job row at all, so without this it did not happen as far as the
queue is concerned — which is the failure the panel exists for.

### Jobs, both halves

```go
observejob.Watch(observe.Default())   // in every process that queues or runs jobs
```

An enqueue and an execution are two entries and they are **linked**. The
dispatch middleware stamps the queueing request's id into the job row's
metadata; the execution middleware reads it back off the row the worker puts
on the context. So an execution names the request that queued it even though
it happened minutes later in another process, and `queued_by` is that link.

The execution deliberately gets its **own** correlation id rather than
reusing the request's. Reusing it would file a worker's queries under a web
request that finished minutes earlier, which reads as one impossibly long
request.

Needs tasker v0.2.1. Before v0.2.0 there was no dispatch hook at all, so a
job queued and never picked up looked exactly like a job nobody queued —
which is the difference between "no worker is running" and "the dispatch call
is not being reached".

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
request to see everything it did, or an entry to see its payload — a panic's
stack and a dump's value live there, and the list shows one line of it. It
tails live over SSE rather than polling, so it is current rather than up to
five seconds stale.

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
| `observe-tagged` | everything carrying a set of tags, across requests |

These supersede `read-log-entries` and `last-error`, which tail a rotating
file and match with `strings.Contains` — so `last-error` finds a request to
`/error-pages`. Keep those as the raw-file escape hatch.

## Tags

A request id groups what one request did. A **tag** groups across requests,
which is the question a correlation id structurally cannot answer, because
every request has a different one.

Put them on the context, once, where you know who this is:

```go
// in a middleware, after auth has resolved the user
ctx = observe.WithTags(r.Context(), "user:"+id, "tenant:"+tenant)
```

Everything recorded under that context carries them — the request entry, its
queries, its cache lookups, its gate checks, and whatever watcher is added
next year. Tagging per watcher instead would mean teaching each one about
authentication and would still miss the ones added later.

Three tags arrive without you doing anything: `failed` on every entry that
went wrong, and `method:GET` / `status:500` on request entries. There is
deliberately no `slow` tag: a threshold that means something for a query
means nothing for a request, and one number across both would be wrong twice.

Filter on them in the dashboard (click a chip), in the list and tail
(`?tags=user:7,failed`), or over MCP (`observe-tagged`). **Every** tag must
match, not any — a reader asking for `user:7` and `failed` wants what that
user broke.

Tags are capped at 12 per entry and 64 characters each, for the reason the
memory store is bounded: what an application knows about a request usually
came from the request.

**There is no index on the column.** It is matched with `LIKE`, which no
btree can serve, and the portable alternatives across Postgres, MySQL and
SQLite are a join table (double the writes on a hot path) or a Postgres-only
`text[]` with GIN. Retention is what bounds the scan — and the migration is
published into your project, so an application with the volume to care can
change the column there.

## Sampling, and turning it down

`OBSERVE_SAMPLE` is a rate between 0 and 1 — and it samples **requests**, not
entries:

```
OBSERVE_SAMPLE=0.1
```

Keeping a tenth of the queries of every request gives you ten percent of a
story about all of them; keeping every query of a tenth of requests gives you
the whole story about some, and the whole story is the one you can debug. The
decision is a hash of the request id, so every entry of one request agrees —
including entries recorded minutes later by a worker that inherited the id.

**The decision is made once, where the correlation id is minted**, and cached
on the context. That is what makes sampling cheap rather than merely correct:
a watcher asks `observe.ShouldRecord(ctx)` before it builds anything, so a
sampled-out statement never has its parameters redacted or its payload
marshalled.

```
BenchmarkQueryWithNoRecorder    8.3 ns/op      0 B/op   0 allocs/op
BenchmarkQuerySampledOut       17.0 ns/op      0 B/op   0 allocs/op
BenchmarkQueryRecorded         3545 ns/op   1473 B/op  14 allocs/op
```

Before the gate existed the middle row was 2,781ns and twelve allocations:
the sampler ran inside `Record`, which is *after* the caller had built the
entry, so a statement about to be discarded paid full price first. At a one
percent rate that saved about a third of the work rather than ninety-nine
percent of it. `bench_hotpath_test.go` is what keeps it honest.

### What sampling never drops

Anything that **failed**, plus exceptions and dumps. A sampled-out request
that broke is the request you most need, and an error view showing a tenth of
the errors is worse than none — it reads as a number rather than as a sample.
A dump was asked for by name, in code somebody wrote.

The cost is worth knowing: a badly broken application samples itself less,
because more of its entries qualify. The queue's drop counter is what bounds
that, and it is reported.

The other limitation is real too. At a low rate a failed request gives you the
request entry and the exception, not the queries that led there — those were
never built. Raise the rate when you need them.

## Leaving it on in production

```
OBSERVE_PROFILE=production
```

One variable rather than five that have to be remembered together. It stops
recording bound parameters, records statements as shapes, floors the log
level at info, and samples a tenth of requests. Anything set explicitly in
code overrides it.

| | development | production |
|---|---|---|
| bound parameters | recorded | not recorded |
| statements | as written | normalised to shapes |
| log records | all | info and above |
| sample rate | everything | a tenth |

What each costs, measured rather than asserted:

```
BenchmarkQueryRecorded               2675 ns/op   1473 B/op   14 allocs/op
BenchmarkQueryRecordedWithoutArgs    1859 ns/op   1321 B/op    8 allocs/op
BenchmarkQueryRecordedInProduction   2628 ns/op   1513 B/op    9 allocs/op
BenchmarkNormaliseSQL                 736 ns/op    192 B/op    1 allocs/op
```

`WithoutArgs` is the saving — 30% and six allocations, because redacting and
stringifying every parameter was the most expensive thing on the path.
Normalising costs about 700ns and one allocation back; it buys smaller stored
and shipped entries rather than time, and it is in the profile because that is
what matters once entries leave the process.

**`WithoutArgs` is not a privacy control on its own.** It drops the bound
parameters, and a value written into the statement *text* is part of the
statement. An ORM binds, so this is rare — hand-written SQL that interpolates
does not. `WithNormalisedStatements` is what removes those, which is the
second reason the profile turns on both.

The log floor lives in the handler's `Enabled`, so a record below it costs an
integer comparison and is never built. That is also its cost: a line refused
there cannot be recovered by turning sampling up, which is why there is no
floor in development.

### Two samplers

`WithRequestSampler` is the coarse gate, over a correlation id, consulted
before anything is built. `WithSampler` is a finer filter over a built entry,
applied within a kept request — "keep a tenth of requests, and within those
drop cache hits". They compose.

Sampled-out entries are counted separately from dropped ones and shown in the
dashboard. Without that number, a list one tenth as long as expected looks
like a broken watcher rather than like the configuration working.

The dashboard also has a **Pause** button. It pauses the process that served
the click, which is exactly right in development — one process, and a pause
is how you read a list that will not stop scrolling. It is **not** a
deployment switch: with several web processes the others keep recording, and
the page says so. There is no `observe:pause` command for the same reason —
a command runs in its own process, so it would pause its own recorder and
report success while the web server carried on.

## Which process recorded this

A shared store across several web processes is the normal deployment, and
without an identity two instances' entries are indistinguishable — so "which
host is slow", "did the new release do this" and "is it only the workers"
have no answer.

```go
&observe.Provider{Source: observe.Source{Release: commit}}
```

`Service` defaults to `APP_NAME`, `Environment` to the profile, `Release` to
`OBSERVE_RELEASE`, and `Host` and `Instance` fill themselves in. **`Release`
is the one worth setting in a deployment**, because it is what makes "did the
new build do this" answerable — and nothing here guesses it, since an entry
labelled with the wrong release sends somebody to the wrong commit.

`Instance` is minted per run, so a restart is visible rather than looking like
continuous history.

It rides in two places. Each entry carries a compact `service/host/instance`
in its own column, because the local dashboard reads rows and has no envelope
to consult. The full structured value goes in the export envelope, once per
batch, where that is the unit.

Filter on it in the dashboard, the list, the tail or the API with
`?source=`. It is a substring match and the rendering puts the longest-lived
part first on purpose, so `shop/` selects a service, `shop/web-7f3` selects a
host, and the whole thing selects one run.

**Upgrading needs the migration again** — `source` is a third published file,
for the same reason `tags` was a second: `MigrationVersion` is fixed so a
republish is a no-op, and editing a stub that has already run changes nothing
for an existing project.

## Shipping entries somewhere else

A `Sink` is the write half of a `Store`, split out so somewhere to *send*
entries does not have to be somewhere to *read them back from*:

```
OBSERVE_EXPORT_URL=https://collector.example.com/v1/entries
OBSERVE_EXPORT_TOKEN=…
```

That wires `observe/export`'s HTTP transport — gzipped JSON envelope, bearer
auth, the source attached once per batch. Or build it yourself:

```go
exporter := export.New(export.JSONL(file))
observe.New(store, observe.WithSinks(exporter))
```

Sinks run **alongside** the store, never instead of it: a sink that fails
must not cost the local dashboard its entries, and a store that fails must
not stop them being shipped. A sink implementing `SinkCloser` is closed with
the recorder, so an exporter flushes what it is holding on a graceful
shutdown rather than taking it to the grave.

### It is a goroutine, not an agent

Laravel Nightwatch ships a separate agent process because PHP cannot do
background work — a request ends and the process is gone, so something else
has to own the buffer and the retries. That is a constraint of the runtime,
not a property of the design, and Go does not have it.

So this runs in the application: nothing extra to deploy, no socket to
supervise, no version skew between an app and an agent. The cost is honest —
an application killed hard loses whatever was still buffered — and `JSONL` is
the escape hatch for anybody who would rather a sidecar tailed a file.

### What a dead collector costs

A bounded buffer, a rising counter, and nothing else. `Record` is an append
under a mutex and returns immediately; the sending happens on the exporter's
own goroutine.

When the buffer fills, the **oldest** entries go, because a collector down
for an hour should come back to the last minute of traffic rather than the
first. After five consecutive failures the circuit opens and sending is
suspended for a minute — backoff alone still retries every batch, and with a
full buffer that is a lot of batches.

A failed batch is **discarded rather than put back**. Requeuing would
head-of-line block the buffer on a batch the destination cannot take, while
newer entries were dropped to hold it. Those entries are counted, because
entries that vanish with no number against them are the thing this package
exists not to do.

Four counters, meaning four different things: `Dropped` is the buffer
overflowing, `Failed` is the network saying nothing, `Refused` is the
destination saying no — a 4xx that is not 429, which retrying will never fix
— and `Sent` is what got through.

`observe/export` is the standard library only, so shipping your telemetry
somewhere costs nothing in dependencies. A test pins that.

## The live tail

The dashboard subscribes to the recorder over SSE instead of polling. Entries
appear as they are recorded, before the store writes them.

A tail that falls behind is **closed, not starved**. Dropping entries and
carrying on would leave a reader watching a stream with holes in it and no
way to tell, which for a debugging tool is the worst of the three outcomes —
so the server sends a `gap` event and ends the stream, and the page says what
happened. `Last-Event-ID` is deliberately not honoured: there is no backlog,
so resuming would be a promise the server cannot keep.

`recorder.Subscribe` is public, so a tail is not only the dashboard's:

```go
entries, stop := recorder.Subscribe(0)
defer stop()
for entry := range entries { … }
```

A subscriber never blocks a recording — the send is non-blocking, the same
trade the entry queue makes and for the same reason.

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

The one exception is `observe.Dump`, which records exactly the value you
hand it. Nothing chose what went into the other kinds, so they are guarded;
a dump was chosen, so it is not. See the note beside it.

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

**Three of these used to be here and are not any more.** Cache operations,
notifications and job enqueues were all uncorrelated because their seams took
no context, and all three now do: `cache` v0.3.0, `notify` v0.3.0 and
`tasker` v0.2.1. If an entry still has no request id, the pin is behind.

**A job execution does not share its queueing request's id**, deliberately.
It carries `queued_by` instead. See **Jobs, both halves** above.

**The event watcher only sees the events you name.** `app.On` takes a name
and the emitter has no "every event", so there is nothing to subscribe to
wholesale.

**A driver offering only the legacy non-context interfaces** records its
statements uncorrelated rather than dropping them, and the entry says
`"correlation":"unavailable"` so you stop looking for a cause.

**Tag filtering is a scan, not an index lookup.** See **Tags** above for why,
and what to change if your volume outgrows it.

**A tail has no backlog.** `Last-Event-ID` is not honoured; a tail that falls
behind is closed and says so.

**Pausing is per-process.** There is no `observe:pause` command, and the
reason is in **Sampling, and turning it down**.

**Sampling is decided per correlation id**, so work with no id — a background
goroutine, a command — is always kept. "Nobody sampled this" does not read as
"this was sampled out".

**Nothing prunes itself.** Run `lemmego run observe:prune` on a schedule.

## Safety

- Off unless `OBSERVE_ENABLED` says otherwise.
- `DashboardAuth` nil means **no access**, not no authentication — the
  dashboard answers 401 to everything until you give it a predicate.
- The dashboard is mounted on the bare mux, so none of your router's
  middleware runs for it. The guard is applied by this package, by hand.
- Set `OBSERVE_SAMPLE` before using this under real traffic.
- The tail is behind the same guard as everything else: an unauthenticated
  stream would be the whole credential dump, live.
- Pausing is a `POST`, so your CSRF middleware applies to it — which is the
  right default for a button that turns the debugger off.

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
