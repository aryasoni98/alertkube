package app

import (
	"sync"
	"time"

	"github.com/aryasoni98/alertkube/internal/alert"
	"github.com/aryasoni98/alertkube/internal/metrics"
)

// deadLetterCap bounds the in-memory dead-letter ring. Dead-lettering is rare
// (only permanently-abandoned deliveries land here), so a small ring is enough
// to answer "what recently failed for good?" without unbounded growth.
const deadLetterCap = 100

// deadLetterEntry is one permanently-abandoned delivery. Only identity and the
// abandonment time are kept - enough to investigate, without retaining the full
// (potentially large) alert Details.
type deadLetterEntry struct {
	Fingerprint string    `json:"fingerprint"`
	Kind        string    `json:"kind"`
	Namespace   string    `json:"namespace"`
	Name        string    `json:"name"`
	Reason      string    `json:"reason"`
	Resolved    bool      `json:"resolved"`
	At          time.Time `json:"at"`
}

// deadLetterLog is a concurrency-safe bounded ring of deliveries the dispatcher
// gave up on (an exhausted resolve, or a fire-once alert with no retry path).
// The dispatcher workers Record from several goroutines; the console reads via
// List. It exists so a permanently-undelivered alert is visible to an operator
// instead of vanishing into a single log line.
type deadLetterLog struct {
	mu      sync.Mutex
	entries []deadLetterEntry
}

func newDeadLetterLog() *deadLetterLog { return &deadLetterLog{} }

// Record appends an abandoned delivery, bumping the metric and trimming the
// ring to deadLetterCap. Called from the dispatch workers.
func (d *deadLetterLog) Record(a *alert.Alert) {
	metrics.DeadLetterTotal.Inc()
	e := deadLetterEntry{
		Fingerprint: a.Fingerprint,
		Kind:        string(a.Kind),
		Namespace:   a.Namespace,
		Name:        a.Name,
		Reason:      a.Reason,
		Resolved:    a.Resolved,
		At:          time.Now(),
	}
	d.mu.Lock()
	d.entries = append(d.entries, e)
	if len(d.entries) > deadLetterCap {
		d.entries = d.entries[len(d.entries)-deadLetterCap:]
	}
	d.mu.Unlock()
}

// List returns a copy of the ring, oldest first.
func (d *deadLetterLog) List() []deadLetterEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]deadLetterEntry, len(d.entries))
	copy(out, d.entries)
	return out
}
