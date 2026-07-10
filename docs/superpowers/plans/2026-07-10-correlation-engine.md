# Correlation Engine (PR1: Annotation) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Infer topological relationships between active alerts, pick a root cause, compute blast radius, and annotate every alert with a `*Correlation` exposed via the API — all opt-in and zero-overhead when disabled.

**Architecture:** A self-contained `internal/topology` package builds resource relationships from its own shared-informer factory (self-disables non-fatally if RBAC is missing). A leader-side `internal/correlate` engine reads the active-alert store on its own ticker, groups alerts by topological connectivity, and writes correlation metadata back to the store. No suppression and no sink rendering in this PR (that is PR2).

**Tech Stack:** Go 1.26, `k8s.io/client-go` shared informers + listers, `prometheus/client_golang`, `gopkg.in/yaml.v3`. Module path `alertkube`.

## Global Constraints

- Go module is `alertkube`; Go 1.26.0 (`go.mod`). Import internal packages as `alertkube/internal/...`.
- **Backward compatibility is mandatory.** Default `correlation.enabled: false` ⇒ byte-for-byte current behavior: no goroutine, no topology informers, no new struct populated.
- `Correlation` is **derived, never persisted**. Do NOT add it to `alert.Snapshot`; do NOT bump `SnapshotVersion`; `ApplyCorrelation` must NOT bump the store `gen` (that would force a ConfigMap save every interval).
- New JSON fields use `omitempty` so existing `/api/alerts` consumers are unaffected.
- Metric names are prefixed `alertkube_` and declared as package vars in `internal/metrics/metrics.go`, then added to the single `prometheus.MustRegister(...)` call in `init()`.
- Correlation informer sync failure is **non-fatal** — log once and run inert, mirroring the CRD-syncer degradation (`internal/app/controller.go:183`). Never `klog.Fatalf`.
- Tests use a fake clientset (`k8s.io/client-go/kubernetes/fake`) + a real `SharedInformerFactory`, matching `internal/watchers/*_test.go`.
- Commit on branch `feat/correlation-engine`. **Stage only files this plan touches** (the working tree has unrelated in-progress changes — never `git add -A`). End every commit message with:
  `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- Run `just test` (unit + race) green before each commit; `just lint` before the final task.

## File Structure

- `internal/alert/correlation.go` (**create**) — `Correlation`, `Ref` types, role consts, deep-clone helper.
- `internal/alert/alert.go` (**modify**) — add `Correlation *Correlation` field; extend `Clone()`.
- `internal/alert/store.go` (**modify**) — add `ApplyCorrelation`.
- `internal/config/config.go` (**modify**) — add `Correlation` config struct + field + `Validate()` bounds.
- `internal/topology/topology.go` (**create**) — `Topology` interface + `Ref`/`Edge` + lister-backed impl + own factory + self-disable.
- `internal/topology/edges.go` (**create**) — ownerRef/node/pvc/service edge queries.
- `internal/correlate/tiers.go` (**create**) — causal-tier table + `tierOf`.
- `internal/correlate/correlate.go` (**create**) — `Recompute`, grouping, blast radius, confidence.
- `internal/correlate/engine.go` (**create**) — `Engine` + `Run` ticker + store apply + metrics.
- `internal/metrics/metrics.go` (**modify**) — 4 correlation metrics + `correlationsHandler` plumbing.
- `internal/app/controller.go` (**modify**) — wire topology + engine when enabled; clear handler on shutdown.
- `internal/app/console.go` (**modify**) — `newCorrelationsHandler`; register in `installConsoleHandlers`.
- `helm/templates/*` + `helm/values.yaml` + `helm/values.schema.json` (**modify**) — RBAC + config.
- Docs: `docs/docs/reference/config-schema.md`, `docs/docs/reference/metrics.md`, `CHANGELOG.md`, `docs/grafana-dashboard.json`.

---

### Task 1: Data model — `Correlation` + `Ref`

**Files:**
- Create: `internal/alert/correlation.go`
- Modify: `internal/alert/alert.go` (Alert struct ~line 158; `Clone()` ~line 187)
- Test: `internal/alert/correlation_test.go`

**Interfaces:**
- Produces: `alert.Correlation`, `alert.Ref` structs; consts `alert.RoleCause`, `alert.RoleEffect`, `alert.RoleStandalone`; `Alert.Correlation *Correlation` field; `(*Correlation).clone()`.

- [ ] **Step 1: Write the failing test**

```go
package alert

import "testing"

func TestCorrelationCloneIsDeep(t *testing.T) {
	a := New(KindPod, "ns", "web-1", "CrashLoopBackOff", SeverityCritical)
	a.Correlation = &Correlation{
		GroupID: "g1", Role: RoleEffect, RootFP: "root", Confidence: 0.9,
		BlastRadius: []Ref{{Kind: "Node", Name: "node-a", Alerting: true}},
	}
	cp := a.Clone()
	cp.Correlation.BlastRadius[0].Name = "MUTATED"
	if a.Correlation.BlastRadius[0].Name != "node-a" {
		t.Fatalf("clone shares BlastRadius slice: got %q", a.Correlation.BlastRadius[0].Name)
	}
	if cp.Correlation == a.Correlation {
		t.Fatal("clone shares Correlation pointer")
	}
}

func TestCorrelationCloneNil(t *testing.T) {
	a := New(KindPod, "ns", "web-1", "X", SeverityInfo)
	if a.Clone().Correlation != nil {
		t.Fatal("nil Correlation must clone to nil")
	}
}
```

- [ ] **Step 2: Run it — expect FAIL** (`Correlation` undefined)

Run: `go test ./internal/alert/ -run TestCorrelationClone -v`
Expected: compile error `undefined: Correlation`.

- [ ] **Step 3: Create `internal/alert/correlation.go`**

```go
package alert

// Correlation is derived, non-persisted context attached to an active alert by
// the correlation engine (internal/correlate). Nil when correlation is disabled
// or the alert stands alone. It is recomputed each interval and never written to
// the persisted Snapshot, so it must not influence dedupe/fingerprint state.
type Correlation struct {
	GroupID     string  `json:"groupId"`
	Role        string  `json:"role"`                      // cause | effect | standalone
	RootFP      string  `json:"rootFingerprint,omitempty"` // "" when this alert is the root
	Reason      string  `json:"reason,omitempty"`
	Confidence  float64 `json:"confidence"`
	BlastRadius []Ref   `json:"blastRadius,omitempty"`
}

const (
	RoleCause      = "cause"
	RoleEffect     = "effect"
	RoleStandalone = "standalone"
)

// Ref identifies a Kubernetes object in a blast radius (alerting or not).
type Ref struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
	Alerting  bool   `json:"alerting"`
}

// clone returns an independent copy (nil stays nil), so the store can hand out
// copies whose BlastRadius slice is not shared with the live alert.
func (c *Correlation) clone() *Correlation {
	if c == nil {
		return nil
	}
	cp := *c
	if c.BlastRadius != nil {
		cp.BlastRadius = make([]Ref, len(c.BlastRadius))
		copy(cp.BlastRadius, c.BlastRadius)
	}
	return &cp
}
```

- [ ] **Step 4: Modify `internal/alert/alert.go`** — add the field to the `Alert` struct (after `Event bool`, ~line 179):

```go
	Event bool
	// Correlation is derived context attached by the correlation engine; nil
	// when correlation is disabled or the alert is standalone. Not persisted.
	Correlation *Correlation
```

And in `Clone()` (after the three map clones, before `return &cp`):

```go
	cp.Correlation = a.Correlation.clone()
	return &cp
```

- [ ] **Step 5: Run tests — expect PASS**

Run: `go test ./internal/alert/ -run TestCorrelation -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/alert/correlation.go internal/alert/alert.go internal/alert/correlation_test.go
git commit -m "feat(alert): add derived Correlation type to Alert

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `Store.ApplyCorrelation`

**Files:**
- Modify: `internal/alert/store.go` (add method near `ActiveList`, ~line 253)
- Test: `internal/alert/store_test.go` (append)

**Interfaces:**
- Consumes: `alert.Correlation` (Task 1).
- Produces: `(*Store).ApplyCorrelation(map[string]*Correlation)`.

- [ ] **Step 1: Write the failing test**

```go
func TestApplyCorrelationSetsAndClears(t *testing.T) {
	s := NewStore(time.Minute, time.Minute, nil)
	a := New(KindPod, "ns", "web-1", "CrashLoopBackOff", SeverityCritical)
	s.ShouldSend(a) // enters active set
	genBefore := s.Generation()

	s.ApplyCorrelation(map[string]*Correlation{
		a.Fingerprint: {GroupID: "g1", Role: RoleEffect, Confidence: 0.9},
	})
	got := s.ActiveList()
	if len(got) != 1 || got[0].Correlation == nil || got[0].Correlation.GroupID != "g1" {
		t.Fatalf("correlation not applied: %+v", got)
	}
	// Derived, not persisted: must NOT bump gen (else a ConfigMap save fires every interval).
	if s.Generation() != genBefore {
		t.Fatalf("ApplyCorrelation bumped gen %d->%d; correlation is not persisted", genBefore, s.Generation())
	}
	// A recompute that drops the linkage clears the stale annotation.
	s.ApplyCorrelation(map[string]*Correlation{})
	if s.ActiveList()[0].Correlation != nil {
		t.Fatal("absent fingerprint must clear Correlation")
	}
}
```

> Note: if `Store.Generation()` does not exist, it is referenced by `sweeper.go:54` (`store.Generation()`) so it does — use it as-is.

- [ ] **Step 2: Run it — expect FAIL** (`ApplyCorrelation` undefined)

Run: `go test ./internal/alert/ -run TestApplyCorrelation -v`
Expected: `undefined: ApplyCorrelation`.

- [ ] **Step 3: Add the method to `store.go`**

```go
// ApplyCorrelation sets each active alert's Correlation from corr (keyed by
// fingerprint), clearing it on any active alert absent from corr so a recompute
// that drops a linkage also drops the stale annotation. It deliberately does NOT
// bump gen: correlation is derived, not persisted, so it must not trigger a
// ConfigMap save every correlation interval.
func (s *Store) ApplyCorrelation(corr map[string]*Correlation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for fp, a := range s.active {
		a.Correlation = corr[fp] // nil (absent key) clears
	}
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/alert/ -run TestApplyCorrelation -race -v`
Expected: PASS, no race.

- [ ] **Step 5: Commit**

```bash
git add internal/alert/store.go internal/alert/store_test.go
git commit -m "feat(alert): Store.ApplyCorrelation writes derived correlation

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: Config — `correlation` block

**Files:**
- Modify: `internal/config/config.go` (Config struct ~line 14; add type; `Validate()` ~line 359)
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Produces: `config.Correlation{Enabled bool; IntervalSeconds, MaxHops, BlastRadiusCap int}`; `Config.Correlation` field. Zero values mean "use engine defaults" (15s / 3 / 50). Validate rejects only out-of-range non-zero values.

- [ ] **Step 1: Write the failing test**

```go
func TestValidateCorrelationBounds(t *testing.T) {
	base := func() *Config {
		c := &Config{Cluster: "c"}
		c.Behavior.MuteSeconds = InformerResyncSeconds + 1
		c.Behavior.ResolveTTLSeconds = InformerResyncSeconds + 1
		return c
	}
	ok := base()
	ok.Correlation = Correlation{Enabled: true, IntervalSeconds: 15, MaxHops: 3, BlastRadiusCap: 50}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid correlation rejected: %v", err)
	}
	bad := base()
	bad.Correlation = Correlation{Enabled: true, MaxHops: 99}
	if err := bad.Validate(); err == nil {
		t.Fatal("maxHops 99 should be rejected")
	}
	// Disabled with junk values must not fail (unused).
	off := base()
	off.Correlation = Correlation{Enabled: false, IntervalSeconds: 1, MaxHops: 99}
	if err := off.Validate(); err != nil {
		t.Fatalf("disabled correlation must skip bounds: %v", err)
	}
}
```

> Adjust the `base()` helper if `Config`'s required fields differ — read the top of `config.go` and any existing `Validate` test for the minimal valid config, and copy that shape.

- [ ] **Step 2: Run it — expect FAIL** (`Correlation` undefined)

Run: `go test ./internal/config/ -run TestValidateCorrelationBounds -v`
Expected: `undefined: Correlation`.

- [ ] **Step 3: Add the type + field + validation**

Add the type (near `Rule`, ~line 228):

```go
// Correlation configures the topology-aware alert correlation engine
// (internal/correlate). Disabled by default. Zero numeric values mean "use the
// engine default". Enabling requires the extra list/watch RBAC in the chart; see
// docs/superpowers/specs/2026-07-10-correlation-engine-design.md.
type Correlation struct {
	Enabled         bool `yaml:"enabled"`
	IntervalSeconds int  `yaml:"intervalSeconds"`
	MaxHops         int  `yaml:"maxHops"`
	BlastRadiusCap  int  `yaml:"blastRadiusCap"`
}
```

Add the field to `Config` (near `Rules`, ~line 211):

```go
	Correlation Correlation `yaml:"correlation"`
```

Add to `Validate()` (before the final `return nil`):

```go
	if c.Correlation.Enabled {
		if v := c.Correlation.IntervalSeconds; v != 0 && v < 5 {
			return fmt.Errorf("correlation.intervalSeconds (%d) must be >= 5", v)
		}
		if v := c.Correlation.MaxHops; v != 0 && (v < 1 || v > 5) {
			return fmt.Errorf("correlation.maxHops (%d) must be in [1,5]", v)
		}
		if v := c.Correlation.BlastRadiusCap; v != 0 && (v < 1 || v > 500) {
			return fmt.Errorf("correlation.blastRadiusCap (%d) must be in [1,500]", v)
		}
	}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/config/ -run TestValidateCorrelationBounds -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): add opt-in correlation config block

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: Topology package — core + ownerRef/node/pvc edges

**Files:**
- Create: `internal/topology/topology.go`
- Create: `internal/topology/edges.go`
- Test: `internal/topology/topology_test.go`

**Interfaces:**
- Consumes: `alert.Ref` (Task 1).
- Produces:
  - `type Edge struct { To alert.Ref; Rel string }` (Rel ∈ `owns|scheduledOn|selects|bound`)
  - `type Topology interface { Neighbors(alert.Ref) []Edge }`
  - `func New(ctx context.Context, clientset kubernetes.Interface, watchNamespace string) Topology` — builds its own factory, starts it, waits for sync (bounded); on failure logs once and returns an inert topology (`Neighbors` → nil).
  - Internal helpers keyed on `alert.Ref{Kind,Namespace,Name}`.

- [ ] **Step 1: Write the failing test** (fake clientset + real factory)

```go
package topology

import (
	"context"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"alertkube/internal/alert"
)

func TestNeighborsOwnerAndNode(t *testing.T) {
	tru := true
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-abc", Namespace: "ns",
		OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", Name: "web", Controller: &tru}},
	}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc-1", Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: &tru}}},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	cs := fake.NewSimpleClientset(rs, pod)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	got := topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-abc-1"})
	if !hasEdge(got, "ReplicaSet", "web-abc", "owns") {
		t.Errorf("missing Pod->ReplicaSet owns edge: %+v", got)
	}
	if !hasEdge(got, "Node", "node-a", "scheduledOn") {
		t.Errorf("missing Pod->Node scheduledOn edge: %+v", got)
	}
	// ReplicaSet -> Deployment via ownerRef.
	rsN := topo.Neighbors(alert.Ref{Kind: "ReplicaSet", Namespace: "ns", Name: "web-abc"})
	if !hasEdge(rsN, "Deployment", "web", "owns") {
		t.Errorf("missing ReplicaSet->Deployment owns edge: %+v", rsN)
	}
}

func hasEdge(edges []Edge, kind, name, rel string) bool {
	for _, e := range edges {
		if e.To.Kind == kind && e.To.Name == name && e.Rel == rel {
			return true
		}
	}
	return false
}
```

- [ ] **Step 2: Run it — expect FAIL** (package does not compile)

Run: `go test ./internal/topology/ -run TestNeighborsOwnerAndNode -v`
Expected: build error `undefined: New`.

- [ ] **Step 3: Create `internal/topology/topology.go`**

```go
// Package topology answers "what is related to what" over the live Kubernetes
// object set, for the correlation engine (internal/correlate). It runs its own
// shared-informer factory so a missing RBAC verb self-disables correlation
// without affecting the core watchers; queries then return empty.
package topology

import (
	"context"
	"time"

	appslisters "k8s.io/client-go/listers/apps/v1"
	batchlisters "k8s.io/client-go/listers/batch/v1"
	corelisters "k8s.io/client-go/listers/core/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"alertkube/internal/alert"
)

// syncTimeout bounds the wait for the correlation factory's initial sync before
// giving up and running inert (non-fatal, unlike the core watcher factory).
const syncTimeout = 30 * time.Second

// Edge is a directed relationship from the queried object to a neighbor.
type Edge struct {
	To  alert.Ref
	Rel string // owns | scheduledOn | selects | bound
}

// Topology answers directed-neighbor queries used by the correlator's BFS.
type Topology interface {
	Neighbors(ref alert.Ref) []Edge
}

type lister struct {
	pods  corelisters.PodLister
	rs    appslisters.ReplicaSetLister
	jobs  batchlisters.JobLister
	svc   corelisters.ServiceLister
	pvc   corelisters.PersistentVolumeClaimLister
	ready bool
}

// New builds and starts the correlation factory. On any sync failure it logs
// once and returns an inert Topology (all queries empty) so correlation
// degrades gracefully instead of crashing the controller.
func New(ctx context.Context, clientset kubernetes.Interface, watchNamespace string) Topology {
	var opts []informers.SharedInformerOption
	if watchNamespace != "" {
		opts = append(opts, informers.WithNamespace(watchNamespace))
	}
	f := informers.NewSharedInformerFactoryWithOptions(clientset, 0, opts...)
	l := &lister{
		pods: f.Core().V1().Pods().Lister(),
		rs:   f.Apps().V1().ReplicaSets().Lister(),
		jobs: f.Batch().V1().Jobs().Lister(),
		svc:  f.Core().V1().Services().Lister(),
		pvc:  f.Core().V1().PersistentVolumeClaims().Lister(),
	}
	f.Start(ctx.Done())
	syncCtx, cancel := context.WithTimeout(ctx, syncTimeout)
	defer cancel()
	for typ, ok := range f.WaitForCacheSync(syncCtx.Done()) {
		if !ok {
			klog.Warningf("correlation disabled: topology informer %v failed to sync (check RBAC for replicasets/services/persistentvolumeclaims); controller continues", typ)
			return &lister{} // inert; ready == false
		}
	}
	l.ready = true
	klog.Info("correlation topology informers synced")
	return l
}
```

- [ ] **Step 4: Create `internal/topology/edges.go`**

```go
package topology

import (
	"k8s.io/apimachinery/pkg/labels"

	"alertkube/internal/alert"
)

// Neighbors returns the direct topological edges out of ref. Unknown kinds and
// the inert (unsynced) topology return nil.
func (l *lister) Neighbors(ref alert.Ref) []Edge {
	if !l.ready {
		return nil
	}
	switch ref.Kind {
	case string(alert.KindPod):
		return l.podEdges(ref)
	case string(alert.KindReplicaSet):
		return ownerEdges(l.rsOwners(ref))
	case string(alert.KindJob):
		return ownerEdges(l.jobOwners(ref))
	}
	return nil
}

func (l *lister) podEdges(ref alert.Ref) []Edge {
	pod, err := l.pods.Pods(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	var edges []Edge
	for _, o := range pod.OwnerReferences {
		edges = append(edges, Edge{To: alert.Ref{Kind: o.Kind, Namespace: ref.Namespace, Name: o.Name}, Rel: "owns"})
	}
	if pod.Spec.NodeName != "" {
		edges = append(edges, Edge{To: alert.Ref{Kind: string(alert.KindNode), Name: pod.Spec.NodeName}, Rel: "scheduledOn"})
	}
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil {
			edges = append(edges, Edge{To: alert.Ref{Kind: string(alert.KindPVC), Namespace: ref.Namespace, Name: v.PersistentVolumeClaim.ClaimName}, Rel: "bound"})
		}
	}
	return edges
}

func (l *lister) rsOwners(ref alert.Ref) []alert.Ref {
	rs, err := l.rs.ReplicaSets(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	return ownerRefs(rs.OwnerReferences, ref.Namespace)
}

func (l *lister) jobOwners(ref alert.Ref) []alert.Ref {
	j, err := l.jobs.Jobs(ref.Namespace).Get(ref.Name)
	if err != nil {
		return nil
	}
	return ownerRefs(j.OwnerReferences, ref.Namespace)
}

func ownerRefs(owners []metaOwner, ns string) []alert.Ref {
	out := make([]alert.Ref, 0, len(owners))
	for _, o := range owners {
		out = append(out, alert.Ref{Kind: o.Kind, Namespace: ns, Name: o.Name})
	}
	return out
}

func ownerEdges(refs []alert.Ref) []Edge {
	out := make([]Edge, 0, len(refs))
	for _, r := range refs {
		out = append(out, Edge{To: r, Rel: "owns"})
	}
	return out
}

// unusedLabelsGuard keeps the labels import wired for Task 5 (service selectors).
var _ = labels.Everything
```

> `metaOwner` is `metav1.OwnerReference`; import `metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"` and change `[]metaOwner` to `[]metav1.OwnerReference`. (Written as an alias here only to keep the block copy-paste-safe; use the real type.)

- [ ] **Step 5: Add missing alert Kinds.** In `internal/alert/alert.go` add `KindReplicaSet Kind = "ReplicaSet"` to the const block and to `Kind.Valid()`. (Node/PVC/Deployment/etc. already exist.)

- [ ] **Step 6: Run tests — expect PASS**

Run: `go test ./internal/topology/ -run TestNeighborsOwnerAndNode -race -v`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/topology/topology.go internal/topology/edges.go internal/topology/topology_test.go internal/alert/alert.go
git commit -m "feat(topology): informer-backed ownerRef/node/pvc edges

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: Topology — Service→Pod selector edges

**Files:**
- Modify: `internal/topology/edges.go`
- Test: `internal/topology/topology_test.go` (append)

**Interfaces:**
- Produces: `selects` edges Service→Pod and Pod→Service (bidirectional so BFS reaches a Service from a failing Pod and vice-versa).

- [ ] **Step 1: Write the failing test**

```go
func TestNeighborsServiceSelectsPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Name: "web-1", Namespace: "ns", Labels: map[string]string{"app": "web"}}}
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "ns"},
		Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}
	cs := fake.NewSimpleClientset(pod, svc)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := New(ctx, cs, "")

	if !hasEdge(topo.Neighbors(alert.Ref{Kind: "Service", Namespace: "ns", Name: "web"}), "Pod", "web-1", "selects") {
		t.Error("Service should select Pod web-1")
	}
	if !hasEdge(topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-1"}), "Service", "web", "selects") {
		t.Error("Pod should back-link to Service web")
	}
}
```

- [ ] **Step 2: Run it — expect FAIL**

Run: `go test ./internal/topology/ -run TestNeighborsServiceSelectsPod -v`
Expected: FAIL (no `selects` edge).

- [ ] **Step 3: Implement selector edges.** In `Neighbors`, add a `Service` case and extend the `Pod` case; replace the `unusedLabelsGuard` var:

```go
	case string(alert.KindService):
		return l.serviceEdges(ref)
```

Add methods:

```go
func (l *lister) serviceEdges(ref alert.Ref) []Edge {
	svc, err := l.svc.Services(ref.Namespace).Get(ref.Name)
	if err != nil || len(svc.Spec.Selector) == 0 {
		return nil
	}
	pods, err := l.pods.Pods(ref.Namespace).List(labels.SelectorFromSet(svc.Spec.Selector))
	if err != nil {
		return nil
	}
	out := make([]Edge, 0, len(pods))
	for _, p := range pods {
		out = append(out, Edge{To: alert.Ref{Kind: string(alert.KindPod), Namespace: ref.Namespace, Name: p.Name}, Rel: "selects"})
	}
	return out
}

// servicesForPod finds Services whose selector matches the pod (back-link).
func (l *lister) servicesForPod(ref alert.Ref) []Edge {
	pod, err := l.pods.Pods(ref.Namespace).Get(ref.Name)
	if err != nil || len(pod.Labels) == 0 {
		return nil
	}
	svcs, err := l.svc.Services(ref.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var out []Edge
	for _, s := range svcs {
		if len(s.Spec.Selector) == 0 {
			continue
		}
		if labels.SelectorFromSet(s.Spec.Selector).Matches(labels.Set(pod.Labels)) {
			out = append(out, Edge{To: alert.Ref{Kind: string(alert.KindService), Namespace: ref.Namespace, Name: s.Name}, Rel: "selects"})
		}
	}
	return out
}
```

In `podEdges`, append the service back-links before `return edges`:

```go
	edges = append(edges, l.servicesForPod(ref)...)
```

Add `KindService Kind = "Service"` to `alert.go` consts + `Valid()`. Remove the `unusedLabelsGuard` line.

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/topology/ -race -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/topology/edges.go internal/topology/topology_test.go internal/alert/alert.go
git commit -m "feat(topology): Service<->Pod selector edges

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: Correlate — causal tiers

**Files:**
- Create: `internal/correlate/tiers.go`
- Test: `internal/correlate/tiers_test.go`

**Interfaces:**
- Consumes: `alert.Alert`, `alert.Kind*` (Task 1/existing).
- Produces: `func tierOf(*alert.Alert) int` (higher = more root-like). Package-private.

- [ ] **Step 1: Write the failing test**

```go
package correlate

import (
	"testing"

	"alertkube/internal/alert"
)

func TestTierOfOrdering(t *testing.T) {
	node := alert.New(alert.KindNode, "", "node-a", "NodeNotReady", alert.SeverityCritical)
	dep := alert.New(alert.KindDeployment, "ns", "web", "Unavailable", alert.SeverityCritical)
	pod := alert.New(alert.KindPod, "ns", "web-1", "CrashLoopBackOff", alert.SeverityCritical)
	hpa := alert.New(alert.KindHPA, "ns", "web", "MaxedOut", alert.SeverityWarning)
	if !(tierOf(node) > tierOf(dep) && tierOf(dep) > tierOf(pod) && tierOf(pod) > tierOf(hpa)) {
		t.Fatalf("tier order wrong: node=%d dep=%d pod=%d hpa=%d", tierOf(node), tierOf(dep), tierOf(pod), tierOf(hpa))
	}
}
```

- [ ] **Step 2: Run it — expect FAIL** (`tierOf` undefined)

Run: `go test ./internal/correlate/ -run TestTierOf -v`

- [ ] **Step 3: Create `tiers.go`**

```go
package correlate

import "alertkube/internal/alert"

// Causal tiers: higher = more likely a root cause. A group's root is its
// highest-tier alert (ties broken by earliest StartsAt, then centrality). v1
// keys on Kind; per-reason refinement is a later slice.
const (
	tierInfra    = 100 // Node
	tierWorkload = 80  // Deployment/StatefulSet/DaemonSet/PVC
	tierPod      = 60  // Pod
	tierEdge     = 40  // HPA (Service later)
	tierUnknown  = 10
)

func tierOf(a *alert.Alert) int {
	switch a.Kind {
	case alert.KindNode:
		return tierInfra
	case alert.KindDeployment, alert.KindStatefulSet, alert.KindDaemonSet, alert.KindPVC:
		return tierWorkload
	case alert.KindPod:
		return tierPod
	case alert.KindHPA:
		return tierEdge
	}
	return tierUnknown
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `go test ./internal/correlate/ -run TestTierOf -v`

- [ ] **Step 5: Commit**

```bash
git add internal/correlate/tiers.go internal/correlate/tiers_test.go
git commit -m "feat(correlate): causal-tier table

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: Correlate — `Recompute` (grouping, root, blast, confidence)

**Files:**
- Create: `internal/correlate/correlate.go`
- Test: `internal/correlate/correlate_test.go`

**Interfaces:**
- Consumes: `topology.Topology` (Task 4/5), `tierOf` (Task 6), `alert.Alert/Correlation/Ref` (Task 1).
- Produces: `func Recompute(alerts []*alert.Alert, topo topology.Topology, maxHops, blastCap int) map[string]*alert.Correlation`.

- [ ] **Step 1: Write the failing test** (fake topology, no k8s)

```go
package correlate

import (
	"testing"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/topology"
)

// fakeTopo returns fixed edges keyed by "Kind/ns/name".
type fakeTopo map[string][]topology.Edge

func (f fakeTopo) Neighbors(r alert.Ref) []topology.Edge { return f[r.Kind+"/"+r.Namespace+"/"+r.Name] }

func TestRecomputeNodeIsRootPodsAreEffects(t *testing.T) {
	node := alert.New(alert.KindNode, "", "node-a", "NodeNotReady", alert.SeverityCritical)
	node.StartsAt = time.Now().Add(-2 * time.Minute)
	p1 := alert.New(alert.KindPod, "ns", "web-1", "CrashLoopBackOff", alert.SeverityCritical)
	p2 := alert.New(alert.KindPod, "ns", "web-2", "CrashLoopBackOff", alert.SeverityCritical)
	p1.StartsAt = time.Now()
	p2.StartsAt = time.Now()
	topo := fakeTopo{
		"Pod/ns/web-1":  {{To: alert.Ref{Kind: "Node", Name: "node-a"}, Rel: "scheduledOn"}},
		"Pod/ns/web-2":  {{To: alert.Ref{Kind: "Node", Name: "node-a"}, Rel: "scheduledOn"}},
		"Node//node-a": {{To: alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-1"}, Rel: "scheduledOn"}, {To: alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-2"}, Rel: "scheduledOn"}},
	}
	got := Recompute([]*alert.Alert{node, p1, p2}, topo, 3, 50)

	if c := got[node.Fingerprint]; c == nil || c.Role != alert.RoleCause {
		t.Fatalf("node should be cause: %+v", c)
	}
	if c := got[p1.Fingerprint]; c == nil || c.Role != alert.RoleEffect || c.RootFP != node.Fingerprint {
		t.Fatalf("p1 should be effect of node: %+v", c)
	}
	if got[node.Fingerprint].GroupID != got[p1.Fingerprint].GroupID {
		t.Fatal("node and p1 must share a group")
	}
}

func TestRecomputeStandalone(t *testing.T) {
	solo := alert.New(alert.KindPod, "ns", "lonely-1", "OOMKilled", alert.SeverityCritical)
	got := Recompute([]*alert.Alert{solo}, fakeTopo{}, 3, 50)
	if c := got[solo.Fingerprint]; c == nil || c.Role != alert.RoleStandalone {
		t.Fatalf("isolated alert should be standalone: %+v", c)
	}
}
```

- [ ] **Step 2: Run it — expect FAIL** (`Recompute` undefined)

Run: `go test ./internal/correlate/ -run TestRecompute -v`

- [ ] **Step 3: Create `correlate.go`**

```go
package correlate

import (
	"sort"

	"alertkube/internal/alert"
	"alertkube/internal/topology"
)

func refOf(a *alert.Alert) alert.Ref {
	return alert.Ref{Kind: string(a.Kind), Namespace: a.Namespace, Name: a.Name}
}
func refKey(r alert.Ref) string { return r.Kind + "/" + r.Namespace + "/" + r.Name }

// bfsDepths returns refKey -> hop distance from `from`, bounded by maxDepth.
func bfsDepths(topo topology.Topology, from alert.Ref, maxDepth int) map[string]int {
	depths := map[string]int{refKey(from): 0}
	type item struct {
		ref alert.Ref
		d   int
	}
	q := []item{{from, 0}}
	for len(q) > 0 {
		cur := q[0]
		q = q[1:]
		if cur.d >= maxDepth {
			continue
		}
		for _, e := range topo.Neighbors(cur.ref) {
			k := refKey(e.To)
			if _, seen := depths[k]; seen {
				continue
			}
			depths[k] = cur.d + 1
			q = append(q, item{e.To, cur.d + 1})
		}
	}
	return depths
}

// Recompute groups alerts by topological reachability (<= maxHops), picks a root
// per group, and returns per-fingerprint correlation metadata. Pure: no I/O
// beyond topo queries; deterministic for a fixed input + topology.
func Recompute(alerts []*alert.Alert, topo topology.Topology, maxHops, blastCap int) map[string]*alert.Correlation {
	out := make(map[string]*alert.Correlation, len(alerts))
	if len(alerts) == 0 {
		return out
	}
	// Index alerting objects by refKey.
	byRefKey := make(map[string]*alert.Alert, len(alerts))
	for _, a := range alerts {
		byRefKey[refKey(refOf(a))] = a
	}
	// Union-find over fingerprints via topological reachability.
	uf := newUnionFind()
	for _, a := range alerts {
		uf.add(a.Fingerprint)
	}
	depthCache := make(map[string]map[string]int, len(alerts))
	for _, a := range alerts {
		d := bfsDepths(topo, refOf(a), maxHops)
		depthCache[a.Fingerprint] = d
		for k := range d {
			if other, ok := byRefKey[k]; ok && other.Fingerprint != a.Fingerprint {
				uf.union(a.Fingerprint, other.Fingerprint)
			}
		}
	}
	// Assemble groups.
	groups := map[string][]*alert.Alert{}
	for _, a := range alerts {
		root := uf.find(a.Fingerprint)
		groups[root] = append(groups[root], a)
	}
	for gid, members := range groups {
		if len(members) == 1 {
			m := members[0]
			out[m.Fingerprint] = &alert.Correlation{GroupID: shortID(gid), Role: alert.RoleStandalone, Confidence: 1}
			continue
		}
		root := pickRoot(members)
		rootDepths := depthCache[root.Fingerprint]
		blast := blastRadius(root, rootDepths, byRefKey, blastCap)
		for _, m := range members {
			if m.Fingerprint == root.Fingerprint {
				out[m.Fingerprint] = &alert.Correlation{
					GroupID: shortID(gid), Role: alert.RoleCause, Confidence: 1,
					Reason: "root cause of " + itoa(len(members)-1) + " correlated alert(s)", BlastRadius: blast,
				}
				continue
			}
			hops := rootDepths[refKey(refOf(m))]
			if hops == 0 {
				hops = maxHops
			}
			out[m.Fingerprint] = &alert.Correlation{
				GroupID: shortID(gid), Role: alert.RoleEffect, RootFP: root.Fingerprint,
				Confidence: confidence(root, m, hops),
				Reason:     "downstream of " + string(root.Kind) + " " + root.Name + " " + root.Reason,
			}
		}
	}
	return out
}

func pickRoot(members []*alert.Alert) *alert.Alert {
	sorted := append([]*alert.Alert(nil), members...)
	sort.SliceStable(sorted, func(i, j int) bool {
		ti, tj := tierOf(sorted[i]), tierOf(sorted[j])
		if ti != tj {
			return ti > tj // higher tier first
		}
		return sorted[i].StartsAt.Before(sorted[j].StartsAt) // earliest first
	})
	return sorted[0]
}

func blastRadius(root *alert.Alert, depths map[string]int, byRefKey map[string]*alert.Alert, cap int) []alert.Ref {
	rk := refKey(refOf(root))
	refs := make([]alert.Ref, 0, len(depths))
	for k := range depths {
		if k == rk {
			continue
		}
		parts := splitKey(k)
		_, alerting := byRefKey[k]
		refs = append(refs, alert.Ref{Kind: parts[0], Namespace: parts[1], Name: parts[2], Alerting: alerting})
	}
	sort.Slice(refs, func(i, j int) bool { // stable, alerting first then by name
		if refs[i].Alerting != refs[j].Alerting {
			return refs[i].Alerting
		}
		return refs[i].Kind+refs[i].Name < refs[j].Kind+refs[j].Name
	})
	if len(refs) > cap {
		refs = refs[:cap]
	}
	return refs
}

func confidence(root, member *alert.Alert, hops int) float64 {
	score := 0.6
	if tierOf(root) > tierOf(member) {
		score += 0.2
	}
	switch {
	case hops <= 1:
		score += 0.15
	case hops == 2:
		score += 0.05
	}
	if !root.StartsAt.After(member.StartsAt) {
		score += 0.05
	} else {
		score -= 0.2
	}
	if score > 1 {
		score = 1
	}
	if score < 0 {
		score = 0
	}
	return score
}
```

- [ ] **Step 4: Create `internal/correlate/util.go`** (union-find + small helpers)

```go
package correlate

import "strconv"

type unionFind struct{ parent map[string]string }

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }
func (u *unionFind) add(x string) {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}
}
func (u *unionFind) find(x string) string {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path halving
		x = u.parent[x]
	}
	return x
}
func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

func shortID(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}
func itoa(n int) string { return strconv.Itoa(n) }
func splitKey(k string) [3]string {
	// k = "Kind/namespace/name"; name may contain no "/" for our kinds.
	var out [3]string
	i := 0
	start := 0
	for j := 0; j < len(k) && i < 2; j++ {
		if k[j] == '/' {
			out[i] = k[start:j]
			i++
			start = j + 1
		}
	}
	out[2] = k[start:]
	return out
}
```

- [ ] **Step 5: Run tests — expect PASS**

Run: `go test ./internal/correlate/ -run TestRecompute -race -v`
Expected: PASS.

- [ ] **Step 6: Add a fuzz test** `internal/correlate/fuzz_test.go` (mirrors `internal/alert/fuzz_test.go` style):

```go
package correlate

import (
	"testing"

	"alertkube/internal/alert"
)

func FuzzRecompute(f *testing.F) {
	f.Add("Pod", "ns", "a", "Node", "node-x")
	f.Fuzz(func(t *testing.T, k1, ns, n1, k2, n2 string) {
		a1 := alert.New(alert.Kind(k1), ns, n1, "R", alert.SeverityInfo)
		a2 := alert.New(alert.Kind(k2), ns, n2, "R", alert.SeverityInfo)
		// Must never panic, and must never mark a standalone/root as effect of itself.
		got := Recompute([]*alert.Alert{a1, a2}, fakeTopo{}, 3, 50)
		for fp, c := range got {
			if c.Role == alert.RoleEffect && c.RootFP == fp {
				t.Fatalf("alert is its own effect: %s", fp)
			}
		}
	})
}
```

- [ ] **Step 7: Run fuzz briefly then commit**

Run: `go test ./internal/correlate/ -run xxx -fuzz FuzzRecompute -fuzztime 10s`
Expected: no crashers.

```bash
git add internal/correlate/correlate.go internal/correlate/util.go internal/correlate/correlate_test.go internal/correlate/fuzz_test.go
git commit -m "feat(correlate): Recompute groups alerts + roots + blast radius

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: Correlate — `Engine` + ticker + metrics

**Files:**
- Create: `internal/correlate/engine.go`
- Modify: `internal/metrics/metrics.go` (var block + `init()` MustRegister)
- Test: `internal/correlate/engine_test.go`

**Interfaces:**
- Consumes: `*alert.Store` (needs `ActiveList()` + `ApplyCorrelation()`), `topology.Topology`, `config.Correlation`.
- Produces: `func New(store *alert.Store, topo topology.Topology, cfg config.Correlation) *Engine`; `(*Engine).Run(ctx context.Context)`; `(*Engine).tick()` (test seam).
- Metrics produced: `metrics.CorrelationGroups` (Gauge), `metrics.CorrelationAlerts` (GaugeVec `{role}`), `metrics.CorrelationComputeSeconds` (Histogram).

- [ ] **Step 1: Add metrics** to `internal/metrics/metrics.go` var block:

```go
	CorrelationGroups = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_correlation_groups", Help: "Active correlation groups."},
	)
	CorrelationAlerts = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "alertkube_correlation_alerts", Help: "Active alerts by correlation role."},
		[]string{"role"},
	)
	CorrelationComputeSeconds = prometheus.NewHistogram(
		prometheus.HistogramOpts{Name: "alertkube_correlation_compute_seconds", Help: "Duration of one correlation recompute pass."},
	)
```

Append them to the `prometheus.MustRegister(...)` call in `init()`.

- [ ] **Step 2: Write the failing test**

```go
package correlate

import (
	"testing"
	"time"

	"alertkube/internal/alert"
	"alertkube/internal/config"
)

func TestEngineTickAnnotatesStore(t *testing.T) {
	store := alert.NewStore(time.Minute, time.Minute, nil)
	node := alert.New(alert.KindNode, "", "node-a", "NodeNotReady", alert.SeverityCritical)
	pod := alert.New(alert.KindPod, "ns", "web-1", "CrashLoopBackOff", alert.SeverityCritical)
	store.ShouldSend(node)
	store.ShouldSend(pod)
	topo := fakeTopo{
		"Pod/ns/web-1": {{To: alert.Ref{Kind: "Node", Name: "node-a"}, Rel: "scheduledOn"}},
		"Node//node-a": {{To: alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-1"}, Rel: "scheduledOn"}},
	}
	e := New(store, topo, config.Correlation{Enabled: true})
	e.tick()

	roles := map[string]string{}
	for _, a := range store.ActiveList() {
		if a.Correlation != nil {
			roles[string(a.Kind)] = a.Correlation.Role
		}
	}
	if roles["Node"] != alert.RoleCause || roles["Pod"] != alert.RoleEffect {
		t.Fatalf("engine did not annotate store: %+v", roles)
	}
}
```

- [ ] **Step 3: Run it — expect FAIL** (`New`/`tick` undefined)

Run: `go test ./internal/correlate/ -run TestEngineTick -v`

- [ ] **Step 4: Create `engine.go`**

```go
package correlate

import (
	"context"
	"time"

	"k8s.io/klog/v2"

	"alertkube/internal/alert"
	"alertkube/internal/config"
	"alertkube/internal/metrics"
	"alertkube/internal/topology"
)

const (
	defaultIntervalSeconds = 15
	defaultMaxHops         = 3
	defaultBlastCap        = 50
)

// Engine recomputes correlation over the active store on a fixed interval and
// writes the results back. Leader-side; runs on its own goroutine, decoupled
// from the sweeper's liveness heartbeat.
type Engine struct {
	store    *alert.Store
	topo     topology.Topology
	interval time.Duration
	maxHops  int
	blastCap int
}

func New(store *alert.Store, topo topology.Topology, cfg config.Correlation) *Engine {
	iv := cfg.IntervalSeconds
	if iv == 0 {
		iv = defaultIntervalSeconds
	}
	mh := cfg.MaxHops
	if mh == 0 {
		mh = defaultMaxHops
	}
	bc := cfg.BlastRadiusCap
	if bc == 0 {
		bc = defaultBlastCap
	}
	return &Engine{store: store, topo: topo, interval: time.Duration(iv) * time.Second, maxHops: mh, blastCap: bc}
}

// Run recomputes on a ticker until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(e.interval)
	defer t.Stop()
	klog.Infof("correlation engine running (interval %s, maxHops %d)", e.interval, e.maxHops)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tick()
		}
	}
}

func (e *Engine) tick() {
	start := time.Now()
	alerts := e.store.ActiveList() // clones; no store lock held during compute
	corr := Recompute(alerts, e.topo, e.maxHops, e.blastCap)
	e.store.ApplyCorrelation(corr)

	groups := map[string]bool{}
	byRole := map[string]float64{alert.RoleCause: 0, alert.RoleEffect: 0, alert.RoleStandalone: 0}
	for _, c := range corr {
		groups[c.GroupID] = true
		byRole[c.Role]++
	}
	metrics.CorrelationGroups.Set(float64(len(groups)))
	for role, n := range byRole {
		metrics.CorrelationAlerts.WithLabelValues(role).Set(n)
	}
	metrics.CorrelationComputeSeconds.Observe(time.Since(start).Seconds())
}
```

- [ ] **Step 5: Run tests — expect PASS**

Run: `go test ./internal/correlate/ ./internal/metrics/ -race -v`
Expected: PASS.

- [ ] **Step 6: Add a benchmark** `internal/correlate/bench_test.go`:

```go
package correlate

import (
	"strconv"
	"testing"

	"alertkube/internal/alert"
	"alertkube/internal/topology"
)

func BenchmarkRecompute1kAlerts(b *testing.B) {
	var alerts []*alert.Alert
	topo := fakeTopo{}
	for i := 0; i < 1000; i++ {
		name := "web-" + strconv.Itoa(i)
		a := alert.New(alert.KindPod, "ns", name, "CrashLoopBackOff", alert.SeverityCritical)
		alerts = append(alerts, a)
		topo["Pod/ns/"+name] = []topology.Edge{{To: alert.Ref{Kind: "Node", Name: "node-a"}, Rel: "scheduledOn"}}
	}
	node := alert.New(alert.KindNode, "", "node-a", "NodeNotReady", alert.SeverityCritical)
	alerts = append(alerts, node)
	edges := make([]topology.Edge, 0, 1000)
	for i := 0; i < 1000; i++ {
		edges = append(edges, topology.Edge{To: alert.Ref{Kind: "Pod", Namespace: "ns", Name: "web-" + strconv.Itoa(i)}, Rel: "scheduledOn"})
	}
	topo["Node//node-a"] = edges
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Recompute(alerts, topo, 3, 50)
	}
}
```

- [ ] **Step 7: Run bench then commit**

Run: `go test ./internal/correlate/ -bench BenchmarkRecompute -benchtime 3x -run xxx`
Expected: completes; note ns/op (should be well under the 15s interval).

```bash
git add internal/correlate/engine.go internal/correlate/engine_test.go internal/correlate/bench_test.go internal/metrics/metrics.go
git commit -m "feat(correlate): interval engine + correlation metrics

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: Wire engine into the controller

**Files:**
- Modify: `internal/app/controller.go` (`runController` ~line 133-176; `shutdown` clear list ~line 461)
- Test: `internal/app/controller_test.go` (append a topology-lifecycle test)

**Interfaces:**
- Consumes: `correlate.New/Run` (Task 8), `topology.New` (Task 4), `cfg.Correlation` (Task 3).

- [ ] **Step 1: Write the failing test** (topology self-disable is the risky path)

```go
func TestTopologyNewSelfDisablesWithoutPanic(t *testing.T) {
	// A fake clientset with no objects still syncs; New must return a usable,
	// inert-safe Topology and never panic on a query.
	cs := fake.NewSimpleClientset()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	topo := topology.New(ctx, cs, "")
	if got := topo.Neighbors(alert.Ref{Kind: "Pod", Namespace: "ns", Name: "x"}); got != nil {
		t.Fatalf("expected no edges for absent pod, got %+v", got)
	}
}
```

Add imports to `controller_test.go`: `"context"`, `"time"`, `"k8s.io/client-go/kubernetes/fake"`, `"alertkube/internal/alert"`, `"alertkube/internal/topology"`.

- [ ] **Step 2: Run it — expect FAIL** (import of unused/undefined until wired; if it already passes because topology exists, that is fine — proceed to wiring)

Run: `go test ./internal/app/ -run TestTopologyNew -v`

- [ ] **Step 3: Wire the engine** in `runController`, after the rule-engine block (~line 176), before `<-ctx.Done()`:

```go
	if cfg.Correlation.Enabled {
		topo := topology.New(ctx, clientset, watchNamespace)
		engine := correlate.New(store, topo, cfg.Correlation)
		wg.Add(1)
		go func() {
			defer wg.Done()
			engine.Run(ctx)
		}()
		klog.Infof("correlation engine enabled")
	}
```

Add imports `"alertkube/internal/correlate"` and `"alertkube/internal/topology"` to `controller.go`.

- [ ] **Step 4: Add handler clear** (for Task 10's route) to `shutdown`'s clear block (~line 461), so a demoted leader stops serving it:

```go
	metrics.ClearCorrelationsHandler()
```

(This method is added in Task 10; if implementing strictly in order, add this line in Task 10 instead. Cross-reference noted so neither task forgets it.)

- [ ] **Step 5: Run the full app package — expect PASS**

Run: `go test ./internal/app/ -race`
Expected: PASS (existing tests unchanged; disabled correlation = no behavior change).

- [ ] **Step 6: Commit**

```bash
git add internal/app/controller.go internal/app/controller_test.go
git commit -m "feat(app): run correlation engine when enabled

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 10: API — `/api/correlations` + `Correlation` on `/api/alerts`

**Files:**
- Modify: `internal/metrics/metrics.go` (add `correlationsHandler` pointer + Set/Clear + route)
- Modify: `internal/app/console.go` (`newCorrelationsHandler`; register in `installConsoleHandlers`)
- Test: `internal/app/console_test.go` (append)

**Interfaces:**
- Produces: `metrics.SetCorrelationsHandler(http.Handler)`, `metrics.ClearCorrelationsHandler()`, route `GET /api/correlations`. `/api/alerts` gains the `Correlation` field automatically (it serializes the stored `*Alert`).

- [ ] **Step 1: Add handler plumbing to `metrics.go`** — mirror `deadLetterHandler` (lines 246-281, 386):

```go
	// correlationsHandler backs GET /api/correlations (read-only, token-gated,
	// leader-scoped): the current set of correlation groups.
	correlationsHandler atomic.Pointer[http.Handler]
```
```go
// SetCorrelationsHandler installs the GET /api/correlations handler.
func SetCorrelationsHandler(h http.Handler) { correlationsHandler.Store(&h) }

// ClearCorrelationsHandler detaches the correlations route on leader loss / shutdown.
func ClearCorrelationsHandler() { correlationsHandler.Store(nil) }
```
In `registerAPIRoutes` (near line 386):
```go
	mux.HandleFunc("/api/correlations", dynamic(&correlationsHandler))
```

- [ ] **Step 2: Write the failing test**

```go
func TestCorrelationsHandlerGroupsAlerts(t *testing.T) {
	store := alert.NewStore(time.Minute, time.Minute, nil)
	node := alert.New(alert.KindNode, "", "node-a", "NodeNotReady", alert.SeverityCritical)
	pod := alert.New(alert.KindPod, "ns", "web-1", "CrashLoopBackOff", alert.SeverityCritical)
	store.ShouldSend(node)
	store.ShouldSend(pod)
	store.ApplyCorrelation(map[string]*alert.Correlation{
		node.Fingerprint: {GroupID: "g1", Role: alert.RoleCause, Confidence: 1},
		pod.Fingerprint:  {GroupID: "g1", Role: alert.RoleEffect, RootFP: node.Fingerprint, Confidence: 0.9},
	})
	// Build deps with a nil token (unauthenticated read path in tests) — follow
	// the existing buildConsoleDeps test pattern in this file.
	h := newCorrelationsHandler(consoleDeps{store: store})
	req := httptest.NewRequest(http.MethodGet, "/api/correlations", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "g1") || !strings.Contains(rr.Body.String(), "\"cause\"") {
		t.Fatalf("body missing group/role: %s", rr.Body.String())
	}
}
```

> Match the existing console_test.go token/deps conventions — read `TestAlertsHandler`-style tests already in the file and copy their `consoleDeps` construction (token handling, `newAlertsHandler`).

- [ ] **Step 3: Run it — expect FAIL** (`newCorrelationsHandler` undefined)

Run: `go test ./internal/app/ -run TestCorrelationsHandler -v`

- [ ] **Step 4: Implement `newCorrelationsHandler`** in `console.go` (mirror `newAlertsHandler`, honoring the same token gate). Group `store.ActiveList()` by `Correlation.GroupID`:

```go
func newCorrelationsHandler(d consoleDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !readAuthorized(r, d) { // use the SAME auth helper newAlertsHandler uses
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		type group struct {
			GroupID string        `json:"groupId"`
			Root    *alert.Alert  `json:"root,omitempty"`
			Members []*alert.Alert `json:"members"`
		}
		groups := map[string]*group{}
		for _, a := range d.store.ActiveList() {
			if a.Correlation == nil || a.Correlation.Role == alert.RoleStandalone {
				continue
			}
			g := groups[a.Correlation.GroupID]
			if g == nil {
				g = &group{GroupID: a.Correlation.GroupID}
				groups[a.Correlation.GroupID] = g
			}
			g.Members = append(g.Members, a)
			if a.Correlation.Role == alert.RoleCause {
				g.Root = a
			}
		}
		out := make([]*group, 0, len(groups))
		for _, g := range groups {
			out = append(out, g)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"correlations": out})
	})
}
```

> Replace `readAuthorized(r, d)` with whatever read-auth check `newAlertsHandler` uses in this file (there is an existing token gate — reuse it verbatim; do not invent a new one).

Register in `installConsoleHandlers` (after `metrics.SetAlertsHandler(...)`, line 182):

```go
	metrics.SetCorrelationsHandler(newCorrelationsHandler(d))
```

- [ ] **Step 5: Verify the `ClearCorrelationsHandler()` line** from Task 9 Step 4 is present in `shutdown`; if not, add it now.

- [ ] **Step 6: Run tests — expect PASS**

Run: `go test ./internal/app/ ./internal/metrics/ -race`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/metrics/metrics.go internal/app/console.go internal/app/console_test.go internal/app/controller.go
git commit -m "feat(api): GET /api/correlations + Correlation on /api/alerts

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 11: Helm RBAC, values, schema, docs

**Files:**
- Modify: `helm/templates/` ClusterRole (the file granting `get;list;watch`)
- Modify: `helm/values.yaml`, `helm/values.schema.json`
- Modify: `docs/docs/reference/config-schema.md`, `docs/docs/reference/metrics.md`, `CHANGELOG.md`, `docs/grafana-dashboard.json`

**Interfaces:** none (packaging/docs).

- [ ] **Step 1: Locate the ClusterRole rules**

Run: `grep -rn "replicasets\|services\|persistentvolumeclaims\|apiGroups" helm/templates/ | head`
Identify the ClusterRole template and its existing rule blocks.

- [ ] **Step 2: Add RBAC rules** (conditional on `.Values.correlation.enabled` so a default install grants nothing new):

```yaml
{{- if .Values.correlation.enabled }}
  # Correlation engine (internal/topology) needs to read relationships.
  - apiGroups: ["apps"]
    resources: ["replicasets"]
    verbs: ["get", "list", "watch"]
  - apiGroups: [""]
    resources: ["services", "persistentvolumeclaims"]
    verbs: ["get", "list", "watch"]
{{- end }}
```

> Pods/nodes/deployments/statefulsets/daemonsets/jobs/cronjobs/hpa list+watch are already granted for the watchers — do not duplicate. Only add what is missing (`replicasets`, `services`, `persistentvolumeclaims`); adjust if the chart already grants any.

- [ ] **Step 3: Add `values.yaml` block:**

```yaml
# Topology-aware alert correlation (annotation only in this release).
# Enabling requires the extra RBAC above and starts a leader-side engine.
correlation:
  enabled: false
  intervalSeconds: 15
  maxHops: 3
  blastRadiusCap: 50
```

- [ ] **Step 4: Add `values.schema.json` entry** under `properties`:

```json
"correlation": {
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "enabled": { "type": "boolean" },
    "intervalSeconds": { "type": "integer", "minimum": 5 },
    "maxHops": { "type": "integer", "minimum": 1, "maximum": 5 },
    "blastRadiusCap": { "type": "integer", "minimum": 1, "maximum": 500 }
  }
}
```

- [ ] **Step 5: Docs.**
  - `config-schema.md`: document the `correlation` block + `/api/correlations`.
  - `metrics.md`: add the 3 correlation metrics.
  - `CHANGELOG.md`: add an "Unreleased" entry: `feat: topology-aware alert correlation (annotation; opt-in via correlation.enabled)`.
  - `grafana-dashboard.json`: add a panel for `alertkube_correlation_groups` and `alertkube_correlation_compute_seconds`.

- [ ] **Step 6: Verify chart + wire the config into Helm→app**

Run: `just helm-lint`
Expected: PASS. Confirm the chart maps `correlation.*` into the app config (the ConfigMap/args template) the same way `grouping.*` is mapped — grep the template that renders `grouping:` and add `correlation:` alongside it.

- [ ] **Step 7: Commit**

```bash
git add helm/ docs/docs/reference/config-schema.md docs/docs/reference/metrics.md CHANGELOG.md docs/grafana-dashboard.json
git commit -m "feat(helm,docs): correlation RBAC, values, schema, docs

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Final verification

- [ ] `just test` — all unit tests + race pass.
- [ ] `just lint` — golangci-lint clean.
- [ ] `just helm-lint` — chart valid.
- [ ] Manual smoke (optional, if a cluster is handy): enable `correlation.enabled`, cordon/delete a node with pods, `GET /api/correlations`, confirm the node is `cause` and its pods are `effect`.
- [ ] Confirm default config (`correlation.enabled: false`) still passes the full suite unchanged — the backward-compat gate.

---

## Deferred to PR2 (Suppression + Rendering)

- `internal/correlate/suppressor.go` — arm/expire effect fingerprints (TTL).
- `event_emitter.go:86` suppression seam (before `r.Route`), consulting the suppressor.
- Config: `suppressEffects` + `minConfidence` (+ Validate bounds).
- Slack sink: root-cause banner + "N related / M impacted" line.
- Metric: `alertkube_alerts_suppressed{reason="correlated"}` (reuse the existing `AlertsSuppressed` vec).

## Self-review (completed by plan author)

- **Spec coverage:** topology (§5)→T4/T5; data model (§4)→T1; store apply (§7)→T2; algorithm (§6 grouping/root/blast/confidence)→T6/T7; engine placement (§3)→T8/T9; API (§7)→T10; config (§8)→T3; metrics (§9)→T8; helm/RBAC (§5)→T11; backward-compat (§12)→final gate; suppression (§6.5) + Slack (§7) → explicitly deferred to PR2. HA/sharding (§10) is behavioral doc, no code. ✓
- **Placeholders:** none; every code step has real code. Two call-outs (`readAuthorized` in T10, `base()` in T3) explicitly instruct copying the existing in-file pattern rather than inventing — flagged, not hidden. ✓
- **Type consistency:** `Recompute(alerts, topo, maxHops, blastCap)` signature identical in T7 (def), T8 (engine), T7 tests, T8 bench. `Topology.Neighbors(alert.Ref) []Edge` identical T4/T5/T7/T8. `alert.Correlation`/`Ref` fields identical T1↔T2↔T7↔T10. `config.Correlation` fields identical T3↔T8↔T11. ✓
