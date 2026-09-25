package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	AlertsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_alerts_total", Help: "Alerts emitted by kind+severity."},
		[]string{"kind", "severity", "reason"},
	)
	AlertsSuppressed = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_alerts_suppressed_total", Help: "Alerts suppressed by reason."},
		[]string{"reason"},
	)
	SinkSendDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "alertkube_sink_send_seconds", Help: "Sink send latency."},
		[]string{"sink", "result"},
	)
	SinkErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_sink_errors_total", Help: "Sink errors by name."},
		[]string{"sink"},
	)
	ActiveAlerts = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_active_alerts", Help: "Count of currently active alerts."},
	)
	// DispatchInflight tracks sink sends currently in progress (including
	// time queued on the rate limiter). A value pinned high for a sink
	// means an alert storm is queueing and rate-limit drops are imminent.
	DispatchInflight = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "alertkube_dispatch_inflight", Help: "Sink sends currently in flight."},
		[]string{"sink"},
	)
	EscalationsTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_escalations_total", Help: "Alerts re-dispatched by escalation rules."},
	)
	// EnrichmentSaturated counts pod alerts shipped without events/logs
	// because the bounded enrichment pool was full. A rising value means
	// alerts are pages arriving "skinny" under storm load - the signal that
	// the enrichWorkers pool is the bottleneck and should be widened.
	EnrichmentSaturated = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_enrichment_saturated_total", Help: "Pod alerts emitted without enrichment because the pool was full."},
	)
	ReceivedAlerts = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_received_alerts_total", Help: "Alerts accepted by the webhook receiver, by status."},
		[]string{"status"},
	)
	// CloudPollTruncated counts polls that hit a source's pagination cap and
	// therefore did not fetch every matching item (e.g. CloudTrail's per-event
	// page limit). A non-zero value means events/resources were dropped that
	// poll - raise the cap or narrow the query.
	CloudPollTruncated = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_cloud_poll_truncated_total", Help: "Cloud polls that hit a pagination cap and dropped remaining items, by source."},
		[]string{"source"},
	)
	// CloudPollErrors counts failed cloud-provider API calls per source
	// (e.g. aws-eks, aws-cloudwatch, aws-ec2). A rising value means a
	// region/credential/permission problem is blinding a cloud source while
	// the in-cluster watchers keep running.
	CloudPollErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_cloud_poll_errors_total", Help: "Cloud provider poll errors by source."},
		[]string{"source"},
	)
	// RuntimeMutations counts control-plane writes made through the console API
	// (e.g. silence create/delete), by action. A non-zero value means the
	// runtime control plane is in use - state that lives outside Git.
	RuntimeMutations = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_runtime_mutations_total", Help: "Control-plane mutations made via the console API, by action."},
		[]string{"action"},
	)
	// StateSnapshotBytes is the size of the last serialized state snapshot. It
	// trends toward the ConfigMap object limit on busy clusters; watch it
	// against StateSaveSkipped to see the cliff (ADR-0003) before saves start
	// being dropped.
	StateSnapshotBytes = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_state_snapshot_bytes", Help: "Size in bytes of the last state snapshot serialized for persistence."},
	)
	// StateSaveSkipped counts state saves dropped because the snapshot exceeded
	// the ConfigMap size guard. A non-zero value means persisted state is going
	// stale and a restart will lose recent resolves/mutes - raise an alert on it.
	StateSaveSkipped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_state_save_skipped_total", Help: "State saves skipped because the snapshot exceeded the size limit."},
	)
	// AlertsDropped counts alerts that failed delivery to every sink on their
	// route (distinct from rate-limited suppression). The dedupe state is rolled
	// back so the next firing retries; a sustained non-zero rate means a sink is
	// persistently failing.
	AlertsDropped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_alerts_dropped_total", Help: "Alerts whose every routed sink failed delivery (dedupe rolled back for retry)."},
	)
	// SinkBreakerOpen is 1 while a sink's circuit breaker is open (sends are
	// short-circuited after sustained failures), 0 otherwise. A value stuck at 1
	// means that sink's endpoint is down and alerts are not reaching it.
	SinkBreakerOpen = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{Name: "alertkube_sink_breaker_open", Help: "1 when a sink's circuit breaker is open (delivery short-circuited)."},
		[]string{"sink"},
	)
	// DispatchQueueDepth is the current number of alerts buffered in the
	// dispatch worker-pool queue. Delivery runs off the informer/producer
	// goroutines through this queue, so a value trending toward its capacity
	// means workers are not draining fast enough (slow sinks / rate limits)
	// and backpressure is imminent.
	DispatchQueueDepth = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_dispatch_queue_depth", Help: "Alerts currently buffered in the dispatch worker-pool queue."},
	)
	// DispatchEnqueueBlocked measures how long an enqueue was parked on a full
	// queue. This is the metric that makes backpressure visible: the queue
	// exists so a slow sink cannot stall Kubernetes event processing, but once
	// it fills, enqueue blocks the calling informer handler and the decoupling
	// is gone. DispatchQueueFull counts that it happened; this shows how bad it
	// got. Buckets span 1ms to ~30s because the interesting range is "briefly
	// blocked" (harmless) vs "seconds per event" (the pipeline is wedged).
	DispatchEnqueueBlocked = prometheus.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "alertkube_dispatch_enqueue_blocked_seconds",
			Help:    "Time an alert enqueue spent blocked on a full dispatch queue (backpressure onto the producer).",
			Buckets: []float64{0.001, 0.01, 0.1, 0.5, 1, 2, 5, 10, 30},
		},
	)
	// DispatchQueueFull counts enqueue attempts that found the queue full and
	// had to block (backpressure). A rising value means the worker pool /
	// sink rate limits are the bottleneck and delivery is falling behind.
	DispatchQueueFull = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_queue_full_total", Help: "Enqueue attempts that blocked because the dispatch queue was full."},
	)
	// DispatchDropped counts alerts dropped because they were enqueued after
	// the dispatcher began shutting down. Non-zero only during a shutdown
	// drain race; a sustained value would indicate a lifecycle bug.
	DispatchDropped = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_dropped_total", Help: "Alerts dropped because they were enqueued after dispatcher shutdown."},
	)
	// OutboxReplayForeign counts outbox records dropped on startup because they
	// belong to another shard. Non-zero only after a shard rebalance
	// (ALERTKUBE_SHARD_TOTAL rollout), where an object's owner moves: replaying
	// such a record would double-page alongside its new owner. A persistently
	// rising value means shard assignment is unstable.
	OutboxReplayForeign = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_outbox_replay_foreign_total", Help: "Outbox records dropped on replay because another shard owns them."},
	)
	// OutboxPending is the current number of deliveries in the durable outbox:
	// accepted by the dispatcher but not yet delivered or dead-lettered. These
	// are persisted and replayed on restart. A value stuck high means delivery
	// is falling behind (slow/failing sinks) and more state must be persisted.
	OutboxPending = prometheus.NewGauge(
		prometheus.GaugeOpts{Name: "alertkube_outbox_pending", Help: "Undelivered deliveries tracked in the durable outbox (persisted + replayed on restart)."},
	)
	// DeadLetterTotal counts deliveries the dispatcher permanently abandoned:
	// a resolve that exhausted its retries (a dangling incident) or a fire-once
	// alert (ephemeral event, group summary, escalation) that failed with no
	// retry path. A non-zero value means alerts reached no sink and will not be
	// retried - inspect /api/deadletter and alert on this.
	DeadLetterTotal = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dead_letter_total", Help: "Deliveries permanently abandoned (no retry path); see /api/deadletter."},
	)
	// DispatchResolveRetries counts resolves re-queued after a failed delivery.
	// Unlike a firing alert, a resolve has no re-trigger, so a lost one would
	// dangle a stateful incident; it is retried a bounded number of times. A
	// rising value means a sink is flaky on the resolve path.
	DispatchResolveRetries = prometheus.NewCounter(
		prometheus.CounterOpts{Name: "alertkube_dispatch_resolve_retries_total", Help: "Resolves re-queued after a failed delivery attempt."},
	)
	// SinkNoop counts sends that were no-ops because the sink's credential
	// (webhook URL / token / routing key) was not configured. A routed sink
	// that no-ops silently drops the alert: with the default route being
	// "slack", a controller started without Slack credentials would otherwise
	// swallow every alert with no signal. A non-zero value means a routed sink
	// is missing its Secret - alert on it.
	SinkNoop = prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "alertkube_sink_noop_total", Help: "Sends that no-oped because the sink's credential was not configured."},
		[]string{"sink"},
	)
)

func init() {
	prometheus.MustRegister(AlertsTotal, AlertsSuppressed, SinkSendDuration, SinkErrors, ActiveAlerts, DispatchInflight, EscalationsTotal, EnrichmentSaturated, ReceivedAlerts, CloudPollErrors, CloudPollTruncated, RuntimeMutations, StateSnapshotBytes, StateSaveSkipped, AlertsDropped, SinkBreakerOpen, SinkNoop, DispatchQueueDepth, DispatchQueueFull, DispatchDropped, DispatchResolveRetries, DeadLetterTotal, OutboxPending, OutboxReplayForeign, DispatchEnqueueBlocked)
}
