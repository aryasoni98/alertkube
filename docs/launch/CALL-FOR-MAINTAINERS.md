# Call for co-maintainers

> For GitHub Discussions, r/kubernetes, and the CNCF Slack (#sandbox /
> relevant SIG channels). Lead with honesty about the bus-factor — it's both the
> truthful framing and the most credible recruiting pitch. People join projects
> that admit they need help, not projects pretending they don't.

---

## Title

**AlertKube is looking for a co-maintainer (Kubernetes alerting controller, Go, Apache-2.0)**

---

## Body

### The honest situation

AlertKube is a Kubernetes multi-resource alerting controller — it watches Pods,
Nodes, Deployments, PVCs, Jobs, DaemonSets, StatefulSets, CronJobs, and HPAs;
classifies conditions; deduplicates and suppresses noise; auto-resolves; and
routes to Slack, PagerDuty, Teams, Opsgenie, Discord, Telegram, and webhooks —
all without a Prometheus stack. It's Apache-2.0, distroless and hardened by
default (least-privilege RBAC, Pod Security `restricted`-compliant), with a Helm
chart, signed multi-arch images + SBOMs, a docs site, ADRs, and a self-health
Grafana dashboard.

It is also **maintained by exactly one person right now.** That's the
bus-factor-1 reality, and I'd rather state it plainly than let someone discover
it the hard way. The engineering is in good shape (clean `govulncheck`/`go vet`,
a recent security audit with 0 critical / 0 high, fuzz + benchmark coverage) —
what the project most needs isn't more code from me, it's a **second
maintainer**, ideally **from a different employer**, to make it something teams
can responsibly depend on.

### Why this matters / the ambition

The roadmap explicitly targets a **CNCF Sandbox** application. The two things
gating that aren't technical — they're (1) a second maintainer from a different
organization for governance neutrality, and (2) independent adopters. The
governance is already written for more than one person:
`GOVERNANCE.md`, `MAINTAINERS.md`, a CNCF Code of Conduct, DCO, and a documented
contribution ladder are in place. There's a real seat to take here, not a
token role.

### What a co-maintainer would own

- **Shared review + release duty** (Conventional Commits + release-please are
  already wired, so releases are low-friction).
- **A domain of your choosing** — the most valuable open areas are below.
- **A genuine vote** in technical direction via the existing governance model,
  not "help with my project" — co-ownership.

### Good first areas (high-impact, well-scoped)

1. **Validate the experimental cloud sources against real accounts.** There's an
   opt-in multi-cloud layer (18 AWS / 6 Azure / 4 GCP control-plane checks)
   currently unit-tested against recorded SDK responses but **not yet proven
   against live AWS/Azure/GCP at scale.** If you run one of these clouds, helping
   prove (and harden) a provider end-to-end is the single most valuable
   contribution available right now. Owning a whole cloud provider is on the
   table.
2. **Expand e2e from CI-on-kind to real clusters + storm/HA scenarios.** The kind
   matrix (1.29–1.31) is green; what's missing is real managed-cluster proof and
   a deliberate alert-storm/load test (there's a known receiver-cardinality
   item to verify under load).
3. **Raise test coverage on the newer `internal/sources` and `internal/rules`
   code** (overall is ~57%; the new surfaces are the thinnest).
4. **The correlation-rules engine** (`internal/rules`: Count / All / Absent
   heartbeat) — a contained, interesting concurrency surface to extend.
5. **CRD path** — there's a design sketch (`docs/design/crd-sketch.md`) for
   teams that outgrow ConfigMap-based config; turning that into a real API is a
   meaty, ownable initiative.

### Also genuinely useful (lower commitment)

- **Try it and add yourself to `ADOPTERS.md`** — even "evaluating," "home lab,"
  or "personal" counts and directly helps the CNCF case.
- **Pick up a `good first issue`**, file bugs, or improve docs.

### How to start

Open a GitHub Discussion or comment here, or reach me via the contact in
`MAINTAINERS.md`. No need to commit to anything upfront — start with a PR or a
"here's the cloud I can help validate," and we'll go from there. If you've ever
wanted maintainer experience on a clean, security-conscious Go/Kubernetes
codebase with a real shot at CNCF, this is a low-bureaucracy way in.

Repo: https://github.com/aryasoni98/alertkube
