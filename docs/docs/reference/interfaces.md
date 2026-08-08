# Extension interfaces

Every extension point AlertKube exposes, with the contract you must satisfy.
All four use the same shape: **declare a narrow interface, register yourself in
`init()`, get wired automatically.** No central list to edit.

| Extend with | Interface | Register via | File to add |
| --- | --- | --- | --- |
| A notification target | `sinks.Sink` | `sinks.Register` | `internal/sinks/<name>.go` |
| A Kubernetes resource kind | `watchers.Watcher` | `watchers.Register` | `internal/watchers/<kind>.go` |
| A cloud service | `sources.Source` | via a `sources.Provider` | `internal/sources/<cloud>/<svc>.go` |
| A cloud provider | `sources.Provider` | `sources.RegisterProvider` | `internal/sources/<cloud>/` |
| A state backend | `persist.Store` | constructor wiring | `internal/persist/` |

---

## `sinks.Sink`

```go
type Sink interface {
    Name() string
    Supports(severity alert.Severity) bool
    Send(ctx context.Context, a *alert.Alert) error
}
```

**Contract**

- `Name()` must be stable: it is a metric label, a routing key in user config,
  and an entry in `config.KnownSinks`. Renaming it is a breaking change.
- `Send` must respect `ctx` — it is deadline-bounded (`perSinkTimeout`, 15s) and
  cancelled at shutdown. A `Send` that ignores cancellation stalls the drain.
- `Send` must be safe for concurrent calls: several dispatch workers can call
  one sink at once during a storm.
- Return an error for a real delivery failure. Do **not** return an error for a
  missing credential — no-op and increment `metrics.SinkNoop` instead, so a
  routed-but-unconfigured sink is visible rather than looking like an outage.
- Read credentials from the environment **on every `Send`**, never cache them at
  construction: that is what makes Secret rotation work without a restart.
- `Supports` gates by severity before the rate limiter and breaker.

**Minimal example**

```go
func init() { Register("mysink", func(c SinkConfig) Sink { return NewMySink(c.Cluster) }) }

type mySink struct{ cluster string; httpClient *http.Client }

func (s *mySink) Name() string                      { return "mysink" }
func (s *mySink) Supports(_ alert.Severity) bool    { return true }

func (s *mySink) Send(ctx context.Context, a *alert.Alert) error {
    url := os.Getenv("MYSINK_WEBHOOK_URL") // read per-send: honors rotation
    if url == "" {
        metrics.SinkNoop.WithLabelValues(s.Name()).Inc()
        return nil // not an error: unconfigured, not broken
    }
    ...
}
```

Then add `"mysink"` to `config.KnownSinks`. A guard test
(`app.TestKnownSinksMatchesRegistry`) fails if you forget — a registered-but-
unknown sink fails config validation, and a known-but-unregistered one is
silently skipped by dispatch.

Most HTTP sinks should embed `webhookSink` rather than implement this directly;
it already handles credential lookup, retries, and body limits.

---

## `watchers.Watcher`

```go
type Watcher interface {
    Name() string
    Setup(ctx context.Context, factory informers.SharedInformerFactory, emit Emit)
}

// Optional: implement when the watcher runs background work off the handler.
type Drainer interface { Drain(ctx context.Context) }
```

**Contract**

- `Setup` registers informer handlers and returns immediately. It must not
  block; the factory has not started yet.
- Every handler must be panic-recovered (`recoverHandler`). A nil-deref in one
  watcher must not take down the controller and silently stop all alerting.
- Apply the namespace filter (`nsFilter.allows`) so the documented
  watched/ignored contract holds for every kind.
- On delete, emit a resolve marker so a deleted-while-firing object does not
  linger until `resolveTTL`.
- Implement `Drainer` if you spawn goroutines: shutdown blocks on `Drain` so
  in-flight work is delivered before final state is saved.

**Registering**

```go
func init() { Register(func(o Opts) Watcher { return NewMyWatcher(o.Config) }) }
```

Return an **untyped nil** to decline a scope — that is how the cluster-scoped
node watcher opts out of a namespace-scoped install. A nil concrete pointer
assigned to `Watcher` is a non-nil interface and will be kept.

Most watchers need only the latest object state and should use `newSimple[T]`,
which owns the struct/`Name`/`Setup` boilerplate. Implement `Watcher` directly
only if you diff old vs. new state (pod, node, cronjob).

---

## `sources.Source` and `sources.Provider`

```go
type Source interface {
    Name() string
    Poll(ctx context.Context, emit Emit)
}

type Provider struct {
    Name        string
    Enabled     func(*config.Config) bool
    PollSeconds func(*config.Config) int
    Build       func(context.Context, *config.Config) ([]Source, error)
}
```

**Contract**

- `Poll` is called on a fixed interval and must return promptly on `ctx`
  cancellation.
- A per-scope API failure records `sources.PollErr` and **continues** to the
  next region/subscription/project. One bad region must not blind the others.
- Emit a resolve for every healthy resource each poll. A resolve for a resource
  with no active alert is a no-op, so this is cheap and keeps state converging.
- Identity convention: the provider scope (region / subscription /
  `project/location`) goes in the alert **Namespace**, the resource id in
  **Name**. That is what makes a resolve target exactly one cloud resource.
- `Build` returning an error is logged and the provider skipped — a cloud-auth
  problem must never take down the Kubernetes watchers.
- Declare a narrow lister interface per service so it unit-tests against canned
  responses without the SDK or live credentials.

Each cloud package has generic helpers that own the fan-out — `pollByRegion`
(AWS), `pollBySubscription` (Azure), `pollByProject` / `projectSource` (GCP).
Use them rather than re-implementing the loop.

---

## `persist.Store`

```go
type Store interface {
    Load(ctx context.Context) (*alert.Snapshot, error)
    Save(ctx context.Context, snap *alert.Snapshot) error
}
```

**Contract**

- `Load` returns `(nil, nil)` when nothing is stored yet. That is the
  cold-start path, **not** an error.
- `Load` returns an error for stored-but-unreadable state. The caller logs it
  and starts cold rather than failing startup.
- `Save` must be safe against concurrent writers. During a leader handoff the
  outgoing and incoming leaders can both write; a naive read-modify-write
  silently drops one snapshot. The ConfigMap backend uses `RetryOnConflict`.
- `Save` may refuse an oversized snapshot with an error. Skipping one save is
  preferable to wedging every subsequent update.
- Implementations must be safe for concurrent use: the sweeper saves on its own
  goroutine while shutdown may issue a final save.

`ConfigMapStore` is the default. Its ceiling is roughly **8k–15k active
alerts** (a ~1MiB object, gzipped) — see
[OPERATIONS](https://github.com/aryasoni98/alertkube/blob/master/docs/OPERATIONS.md).
Growing past that means a different backend, which is what this interface is
for.
