package metrics

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

// descRE pulls the metric name and variable labels out of Desc.String().
var descRE = regexp.MustCompile(`fqName: "([^"]+)".*variableLabels: \{([^}]*)\}`)

// TestCollectorsOnDefaultRegistry pins every exported collector's name and
// label names, and that each one is registered on the default registry that
// promhttp.Handler serves. Renaming a series breaks dashboards and alert
// rules; a collector left unregistered silently never appears on /metrics.
func TestCollectorsOnDefaultRegistry(t *testing.T) {
	cases := []struct {
		c    prometheus.Collector
		want string
	}{
		{AlertsTotal, "alertkube_alerts_total{kind,severity,reason}"},
		{AlertsSuppressed, "alertkube_alerts_suppressed_total{reason}"},
		{SinkSendDuration, "alertkube_sink_send_seconds{sink,result}"},
		{SinkErrors, "alertkube_sink_errors_total{sink}"},
		{ActiveAlerts, "alertkube_active_alerts{}"},
		{DispatchInflight, "alertkube_dispatch_inflight{sink}"},
		{EscalationsTotal, "alertkube_escalations_total{}"},
		{EnrichmentSaturated, "alertkube_enrichment_saturated_total{}"},
		{ReceivedAlerts, "alertkube_received_alerts_total{status}"},
		{CloudPollTruncated, "alertkube_cloud_poll_truncated_total{source}"},
		{CloudPollErrors, "alertkube_cloud_poll_errors_total{source}"},
		{RuntimeMutations, "alertkube_runtime_mutations_total{action}"},
		{StateSnapshotBytes, "alertkube_state_snapshot_bytes{}"},
		{StateSaveSkipped, "alertkube_state_save_skipped_total{}"},
		{AlertsDropped, "alertkube_alerts_dropped_total{}"},
		{SinkBreakerOpen, "alertkube_sink_breaker_open{sink}"},
		{DispatchQueueDepth, "alertkube_dispatch_queue_depth{}"},
		{DispatchEnqueueBlocked, "alertkube_dispatch_enqueue_blocked_seconds{}"},
		{DispatchQueueFull, "alertkube_dispatch_queue_full_total{}"},
		{DispatchDropped, "alertkube_dispatch_dropped_total{}"},
		{OutboxReplayForeign, "alertkube_outbox_replay_foreign_total{}"},
		{OutboxPending, "alertkube_outbox_pending{}"},
		{DeadLetterTotal, "alertkube_dead_letter_total{}"},
		{DispatchResolveRetries, "alertkube_dispatch_resolve_retries_total{}"},
		{SinkNoop, "alertkube_sink_noop_total{sink}"},
	}
	pinned := map[string]bool{}
	for _, tc := range cases {
		ch := make(chan *prometheus.Desc, 1)
		tc.c.Describe(ch)
		close(ch)
		m := descRE.FindStringSubmatch((<-ch).String())
		if m == nil {
			t.Fatalf("%s: cannot parse descriptor", tc.want)
		}
		if got := m[1] + "{" + m[2] + "}"; got != tc.want {
			t.Errorf("descriptor = %s, want %s", got, tc.want)
		}
		pinned[m[1]] = true

		var are prometheus.AlreadyRegisteredError
		err := prometheus.DefaultRegisterer.Register(tc.c)
		if !errors.As(err, &are) || are.ExistingCollector != tc.c {
			t.Errorf("%s: not registered on the default registry (Register returned %v)", tc.want, err)
		}
	}

	// Every collector declared in this package must be pinned. This reads the
	// source rather than gathering the registry: Gather only returns families
	// that have a sample, so a Vec with no children yet would slip past it.
	declared := promautoNames(t)
	for _, name := range declared {
		if !pinned[name] {
			t.Errorf("unpinned collector %s declared via promauto", name)
		}
	}
	if len(declared) != len(cases) {
		t.Errorf("package declares %d promauto collectors, pinned %d", len(declared), len(cases))
	}

	// Belt and braces for anything registered outside a promauto call: no
	// alertkube_ family that has emitted a sample may be unpinned either.
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	for _, f := range families {
		if name := f.GetName(); strings.HasPrefix(name, "alertkube_") && !pinned[name] {
			t.Errorf("unpinned collector %s on the default registry", name)
		}
	}
}

// promautoNames returns the Opts.Name of every promauto.New* call in the
// package's non-test source files. A call whose name is not a string literal
// fails the test, so a collector cannot dodge the pin list by construction.
func promautoNames(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, e.Name(), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", e.Name(), err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "promauto" {
				return true
			}
			name, ok := optsName(call)
			if !ok {
				t.Errorf("%s: promauto.%s call without a literal Opts.Name", e.Name(), sel.Sel.Name)
				return true
			}
			names = append(names, name)
			return true
		})
	}
	return names
}

// optsName extracts the string literal assigned to Name in the call's Opts.
func optsName(call *ast.CallExpr) (string, bool) {
	if len(call.Args) == 0 {
		return "", false
	}
	lit, ok := call.Args[0].(*ast.CompositeLit)
	if !ok {
		return "", false
	}
	for _, e := range lit.Elts {
		kv, ok := e.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if k, ok := kv.Key.(*ast.Ident); !ok || k.Name != "Name" {
			continue
		}
		v, ok := kv.Value.(*ast.BasicLit)
		if !ok || v.Kind != token.STRING {
			return "", false
		}
		s, err := strconv.Unquote(v.Value)
		return s, err == nil
	}
	return "", false
}

// TestSuppressReasonValues pins the documented reason label values. Changing
// one renames a series that operators' alert rules match on.
func TestSuppressReasonValues(t *testing.T) {
	for got, want := range map[string]string{
		SuppressGrouped:      "grouped",
		SuppressForeignShard: "foreign_shard",
		SuppressMuted:        "muted",
		SuppressStartup:      "startup",
		SuppressSilenced:     "silenced",
		SuppressMaintenance:  "maintenance",
		SuppressInhibited:    "inhibited",
		SuppressCircuitOpen:  "circuit_open",
		SuppressRateLimited:  "ratelimited",
	} {
		if got != want {
			t.Errorf("reason %q, want %q", got, want)
		}
	}
}
