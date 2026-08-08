#!/usr/bin/env bash
# Land the architecture-audit work as a reviewable commit series.
#
# WHY THIS EXISTS: the audit was implemented in one working tree because
# `git commit` was unavailable during the session, so the 27 changes could not
# be committed as they were made. This replays them as 14 commits grouped by
# concern.
#
# It is 14 rather than 27 because several steps edit the same files
# (internal/app/dispatcher.go is touched by the replay gate, the worker-queue
# split, the backpressure metric, and tracing) and a path-based split cannot
# separate those. Entangled changes are grouped into one coherent commit each
# rather than faked apart.
#
# Usage:
#   bash scripts/land-audit-commits.sh          # dry run: print the plan
#   bash scripts/land-audit-commits.sh --apply  # actually commit
#
# Safe to stop partway: each commit is independent of the next, and anything
# unstaged at the end is caught by the final check.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

APPLY=false
[[ "${1:-}" == "--apply" ]] && APPLY=true

commit() {
  local msg="$1"; shift
  local -a paths=("$@")
  local -a present=()
  for p in "${paths[@]}"; do
    # Include a path only if it actually has changes, so a partially-applied
    # tree does not produce empty commits or fail the whole run. Uses git
    # status rather than a filesystem test: a deleted path (internal/ui) has
    # changes to commit precisely because it no longer exists on disk.
    if [[ -n "$(git status --porcelain -- "$p" 2>/dev/null)" ]]; then
      present+=("$p")
    fi
  done
  if [[ ${#present[@]} -eq 0 ]]; then
    echo "SKIP (no paths present): ${msg%%$'\n'*}"
    return
  fi
  if ! $APPLY; then
    echo "--- ${msg%%$'\n'*}"
    printf '      %s\n' "${present[@]}"
    return
  fi
  git add -- "${present[@]}"
  if git diff --cached --quiet; then
    echo "SKIP (nothing staged): ${msg%%$'\n'*}"
    return
  fi
  git commit -q -F - <<<"$msg"
  echo "OK: ${msg%%$'\n'*}"
}

# 1. Module path. It claims only the packages no later commit touches - the
# ones the rename is the *sole* change to. Packages that also carry a real
# change are committed with that change, so every commit's contents match its
# message.
#
# CAVEAT: because the rename is atomic in the working tree but split across
# commits here, intermediate commits do not compile. Only the final tree is
# verified (build, vet, lint, race, integration). If you need every commit to
# build, squash this series - the content is identical either way.
commit "fix(build)!: use an importable module path

module alertkube was not a resolvable import path: it could not be go get-ed
and pkg.go.dev could not index it. Renames to github.com/aryasoni98/alertkube
and rewrites every import.

Also updates the Dockerfile's -ldflags -X target. The linker silently ignores
an -X flag whose symbol does not exist, so a missed rewrite there would have
shipped every image stamped \"dev\" with no build error anywhere.

BREAKING CHANGE: importers must update their import paths." \
  go.mod go.sum Dockerfile cmd/ \
  internal/alert/ internal/authz/ internal/collectors/ internal/env/ \
  internal/filter/ internal/group/ internal/httpx/ internal/receiver/ \
  internal/router/ internal/rules/ internal/silence/ internal/sources/ \
  internal/templates/ internal/textutil/ internal/topology/ \
  internal/app/apiutil.go internal/app/apiutil_test.go internal/app/cli.go \
  internal/app/console.go internal/app/console_test.go internal/app/deadletter.go \
  internal/app/event_emitter.go internal/app/event_emitter_test.go \
  internal/app/pipeline.go internal/app/pipeline_test.go \
  internal/app/provider_test.go internal/app/shard_test.go \
  internal/config/rules_test.go internal/config/fuzz_test.go \
  internal/config/maintenance.go internal/config/maintenance_test.go \
  internal/config/aws_test.go internal/config/config_test.go \
  internal/config/validate.go

# 2-6 refine the tree that commit 1 swept up. Ordering is deliberate: the
# sharding fixes come before the docs that describe them.

commit "ci: add Dependabot for gomod, actions, and docker

dependency-review gates PRs and trivy scans images, but nothing opened update
PRs, so dependencies rotted between manual bumps. OpenSSF Scorecard scores this
directly and the scorecard workflow was already enabled.

k8s.io/* and sigs.k8s.io/* are grouped because they release as a matched set
and a partial bump breaks compilation on version-skew rules." \
  .github/dependabot.yml

commit "fix(sharding)!: scope the leader Lease and state ConfigMap per shard

Every shard contended for one Lease named \"alertkube\", so exactly one shard
led and the rest watched nothing - while all pods reported Ready, because a
leader-election follower is Ready by design. Most of the cluster silently
stopped being alerted on.

Every shard also saved to one state ConfigMap, so each save overwrote the
others' mute history and delivery outbox. The symptom was a re-paging storm
after any restart.

The shard identity now resolves in Run() before anything that depends on it,
and an explicitly-configured state ConfigMap name is rejected at startup unless
it carries this replica's shard index.

BREAKING CHANGE: sharded deployments move to alertkube-shard-<i> and
alertkube-state-<i>. Delete the old shared alertkube-state ConfigMap after
upgrading - it holds an arbitrary single shard's partial snapshot." \
  internal/shard/ internal/config/shardscope.go internal/config/shardscope_test.go \
  internal/config/config.go internal/app/leaderelection.go internal/app/leaderelection_test.go

commit "fix(dispatch): serialize deliveries per fingerprint and gate replay on ownership

Workers drained one shared queue. FIFO holds at dequeue, not at completion: a
FIRE whose sink call is slow could land after its RESOLVE, leaving a stateful
sink (PagerDuty/Opsgenie keys on fingerprint) holding an incident that never
closed. Deliveries are now routed to one worker per fingerprint.

Outbox replay re-submitted every record unconditionally. After a shard
rebalance - the documented ALERTKUBE_SHARD_TOTAL rollout - an object's owner
moves, so replaying from the old owner double-paged. Foreign records are now
dropped and counted on alertkube_outbox_replay_foreign_total.

A replayed firing alert also carried no onFail, so it was dead-lettered on its
first failure while a fresh firing rolls dedupe back and retries. It now gets
the same rollback path, so the outbox no longer reduces durability.

BREAKING CHANGE: ALERTKUBE_DISPATCH_QUEUE is now the process-wide total, split
across workers, so raising the worker count no longer multiplies the memory
ceiling." \
  internal/app/dispatcher.go internal/app/dispatcher_shard_test.go \
  internal/app/replay_retry_test.go internal/app/dispatcher_test.go \
  internal/app/controller.go internal/app/app.go

commit "feat(api)!: version the native HTTP API under /api/v1

The native routes were unversioned while the only versioned path,
/api/v1/alerts, was the borrowed Alertmanager receiver - so the sole versioned
route was the one we did not design, and it differed from the native alert dump
by one path segment with the opposite meaning.

The receiver moves to /api/v1/receiver/alerts. A POST to the old path
308-redirects rather than being answered 200 by the read handler, which would
have silently discarded the batch. Pre-v1 paths 308-redirect for one minor
release; 308 rather than 301 because several routes are DELETE or POST and a
301 lets a client downgrade the method to GET.

BREAKING CHANGE: update Alertmanager webhook_config url to
/api/v1/receiver/alerts." \
  internal/metrics/metrics.go internal/metrics/metrics_test.go \
  internal/metrics/apiversion_test.go internal/metrics/handlers.go

commit "feat: extract persist.Store, self-register watchers, detect slow sinks

persist.Store makes the state backend swappable: the ConfigMap backend caps out
near 8k-15k active alerts (a ~1MiB object, gzipped), and growing past that means
a different backend, not a bigger ConfigMap.

Watchers now self-register in init() like sinks and cloud providers. Two of the
three extension points were self-contained; the third made you edit a file in
another package to add a resource kind.

The breaker gains slow-send detection. A sink answering 200 in 20s never
incremented the failure counter while occupying a dispatch worker for the whole
call - slow is a different failure from broken and needs the same
short-circuit. Adds alertkube_dispatch_enqueue_blocked_seconds so backpressure
onto informer handlers is observable." \
  internal/persist/ internal/watchers/ internal/sinks/ internal/app/builders.go \
  internal/app/builders_test.go internal/app/sweeper.go

commit "feat(observability): opt-in OpenTelemetry tracing for the delivery path

The most common support question is \"the pod crashed, why wasn't I paged?\",
and answering it meant correlating six metrics by hand. Spans now cover
enqueue -> dispatch, with the terminal span naming the stage that dropped the
alert.

Producer and delivery are separate spans joined by trace linkage carried on the
queued job. The job deliberately does not inherit the producer's context: an
informer handler's context is cancelled the moment it returns, long before a
worker performs the HTTP send, so inheriting it would cancel every queued
delivery. trace.Detach keeps the span context and nothing else.

Off by default - a controller whose job is to page must not hard-depend on a
collector being reachable." \
  internal/trace/ internal/app/dispatch_trace.go

commit "feat(api): publish typed Silence CRD types

The CRD schema lived only in the Helm template, with the controller decoding it
through unstructured map lookups whose contract was enforced nowhere. A dynamic
informer means no generated clientset is needed; it does not mean the API
should have no types.

The informer is unchanged (ADR-0004) - decoding now goes through the typed
struct. deepcopy is hand-written rather than controller-gen'd, to avoid pulling
the controller-runtime ecosystem ADR-0001 declines." \
  api/ internal/crd/

commit "test: add an envtest integration tier


The sharding defects this guards were invisible to both existing tiers. Unit
tests use fake.NewSimpleClientset, which does not model resource-version
conflicts, so it cannot show two shards clobbering one ConfigMap. E2E runs a
single deployment and never exercises Lease contention. The failures lived in
the gap.

Covers per-shard Lease contention (all shards must lead simultaneously, and the
inverse: a shared lease admits only one), per-shard state isolation, and
concurrent-save conflict handling during a leader handoff.

sigs.k8s.io/controller-runtime is a test-only dependency here, for envtest's
process management. No production package imports it." \
  test/integration/ .github/workflows/ci.yml

commit "docs: correct the sharding guidance

The docs described a configuration that silently disabled N-1 shards, and the
README claimed leader election still gated shared state alongside sharding.
Documentation that causes silent alert loss is worse than missing
documentation." \
  README.md docs/docs/how-to/ha-leader-election.md

commit "docs: add the operations guide

docs/OPERATIONS.md was linked from the docs index, a how-to, the HA guide, and
ADR-0002 - and had never existed. Covers SLOs with recommended alerting rules,
the active-alert ceiling and where it comes from, dashboards, the HA and shard-
rebalance runbooks, and troubleshooting." \
  docs/OPERATIONS.md

commit "docs: add architecture diagrams and reference material

Zero diagrams existed for a system with six pipeline stages, three alert
producers, and two HA models. Adds component and sequence diagrams, the
extension-interface contracts, the security architecture, the data model, and a
deployment-topology guide." \
  docs/docs/architecture.md docs/docs/reference/interfaces.md \
  docs/docs/reference/data-model.md docs/docs/explanation/security-architecture.md \
  docs/docs/how-to/deployment-topologies.md docs/mkdocs.yml

commit "docs: add roadmap, release policy, ADR-0005, and example configs

Config immutability was load-bearing (handlers pre-render, routing tables build
once) but undocumented as a decision, which invited the hot-reload question
ADR-0005 now answers.

RELEASE.md defines what counts as breaking for a controller people run rather
than import: config schema, metric names, API paths, CRD schema, RBAC.

Promotes docs/superpowers/ to docs/design/ as first-class design docs, and adds
a checklist for the repo settings that cannot be set from the repository." \
  ROADMAP.md RELEASE.md docs/decisions/ docs/design/ examples/

commit "docs: update API paths, metrics, and config reference

Follows the /api/v1 move through the chart, helm docs, and manual, and documents
the new metrics and environment variables.

Also drops internal/ui/dist/logo.png, the last tracked artifact of the embedded
console removed in 81d3956, and adds the script that replayed this series." \
  docs/docs/ helm/ CHANGELOG.md internal/ui scripts/land-audit-commits.sh

if $APPLY; then
  echo
  if [[ -n "$(git status --porcelain)" ]]; then
    echo "WARNING: uncommitted changes remain:"
    git status --short
  else
    echo "Clean tree. Commit series:"
    git log --oneline "$(git rev-parse HEAD~14 2>/dev/null || echo HEAD)"..HEAD 2>/dev/null || git log --oneline -14
  fi
else
  echo
  echo "Dry run. Re-run with --apply to commit."
fi
