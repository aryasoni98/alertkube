// Package group folds alert storms: the first alert of a group passes
// through immediately (pages stay fast), subsequent alerts in the same
// group within the window are absorbed and surface as one summary alert
// when the window closes. 200 crashlooping pods become two messages
// instead of 200.
package group

import (
	"context"
	"fmt"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
	"time"

	"k8s.io/klog/v2"

	"github.com/aryasoni98/alertkube/v2/internal/alert"
)

// defaultBy is the group identity when config does not override it.
var defaultBy = []string{"kind", "namespace", "reason", "severity"}

// memberListCap bounds how many member names a summary's text lists.
const memberListCap = 10

// memberDetailCap bounds the full list in the summary's Details.
const memberDetailCap = 50

// Grouper tracks open group windows. Safe for concurrent use.
type Grouper struct {
	window time.Duration
	by     []string
	flush  func(*alert.Alert)

	mu      sync.Mutex
	buckets map[string]*bucket
	// closing is set by FlushAll at shutdown drain so Offer stops opening
	// or joining windows that nothing would ever flush.
	closing bool
}

type bucket struct {
	first    alert.Alert
	members  []string
	count    int
	deadline time.Time
}

// New builds a Grouper. flush receives each summary alert; it is invoked
// without the Grouper lock held.
func New(window time.Duration, by []string, flush func(*alert.Alert)) *Grouper {
	if len(by) == 0 {
		by = defaultBy
	}
	return &Grouper{
		window:  window,
		by:      slices.Clone(by),
		flush:   flush,
		buckets: map[string]*bucket{},
	}
}

// Offer reports whether the caller should dispatch the alert now.
// false means the alert was absorbed into a pending summary. Triggers and
// resolves group in separate key spaces so a resolve wave folds into its
// own "N resolved" summary instead of reopening the trigger window.
func (g *Grouper) Offer(a *alert.Alert) bool {
	key := a.GroupKey(g.by)
	if a.Resolved {
		key += "|resolved"
	}
	now := time.Now()

	g.mu.Lock()
	if g.closing {
		// Shutdown drain is underway (FlushAll holds, or has held, the
		// lock and set this). Opening or joining a window now would strand
		// the alert in a bucket the final flush already passed. Pass it
		// through so the caller dispatches it directly instead.
		g.mu.Unlock()
		return true
	}
	b, ok := g.buckets[key]
	if !ok || now.After(b.deadline) {
		// Retain only the summary's identity; no caller-owned maps or logs.
		g.buckets[key] = &bucket{
			first: alert.Alert{
				Kind: a.Kind, Namespace: a.Namespace, Name: a.Name,
				Reason: a.Reason, Severity: a.Severity, Cluster: a.Cluster,
				Resolved: a.Resolved,
			},
			deadline: now.Add(g.window),
		}
		g.mu.Unlock()
		if ok {
			// The old window expired before the flusher ran.
			g.emitSummary(b)
		}
		return true
	}
	b.count++
	if len(b.members) < memberDetailCap {
		b.members = append(b.members, a.Namespace+"/"+a.Name)
	}
	g.mu.Unlock()
	return false
}

// Run flushes expired windows until ctx is cancelled, then drains every
// open bucket so absorbed alerts are not lost on shutdown. A panic in one
// tick is logged and recovered, so later ticks still flush. The drain is a
// recovered defer: it always runs, and a panic in it cannot crash the process
// before the dispatch drain and final state save.
func (g *Grouper) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	defer recovered("final flush", g.FlushAll)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			recovered("flush", func() { g.flushExpired(time.Now()) })
		}
	}
}

// recovered runs fn and logs a panic in it with its stack instead of letting
// it end Run's goroutine.
func recovered(where string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			klog.Errorf("grouper %s panic: %v\n%s", where, r, debug.Stack())
		}
	}()
	fn()
}

func (g *Grouper) flushExpired(now time.Time) {
	g.mu.Lock()
	var expired []*bucket
	for key, b := range g.buckets {
		if now.After(b.deadline) {
			expired = append(expired, b)
			delete(g.buckets, key)
		}
	}
	g.mu.Unlock()
	g.emitSummaries("flush", expired)
}

// FlushAll closes every open window immediately.
func (g *Grouper) FlushAll() {
	g.mu.Lock()
	// Mark closing first: any Offer racing this drain (including a re-entrant
	// Offer triggered by emitSummary -> flush -> dispatchResolved below) then
	// passes through rather than opening a bucket that nothing flushes.
	g.closing = true
	var all []*bucket
	for key, b := range g.buckets {
		all = append(all, b)
		delete(g.buckets, key)
	}
	g.mu.Unlock()
	g.emitSummaries("final flush", all)
}

// emitSummaries flushes each bucket under its own recover. The buckets are
// already out of the map, so a panic in one summary must not skip the rest:
// a skipped bucket is never flushed, and its chat-only members were muted
// when absorbed, so they would reach no one.
func (g *Grouper) emitSummaries(where string, buckets []*bucket) {
	for _, b := range buckets {
		recovered(where, func() { g.emitSummary(b) })
	}
}

// emitSummary builds and flushes the summary alert for a bucket that
// absorbed at least one member. A bucket whose window passed with no
// absorptions produces nothing - the pass-through alert already told the
// whole story.
func (g *Grouper) emitSummary(b *bucket) {
	n := b.count
	if n == 0 {
		return
	}
	f := b.first
	s := alert.New(f.Kind, f.Namespace, fmt.Sprintf("%d-grouped", n), f.Reason, f.Severity)
	s.Cluster = f.Cluster
	s.Resolved = f.Resolved
	s.Labels["alertkube-grouped"] = "true"

	verb := "fired"
	if f.Resolved {
		verb = "resolved"
	}
	listed := b.members
	suffix := ""
	if len(listed) > memberListCap {
		suffix = fmt.Sprintf(" (+%d more)", n-memberListCap)
		listed = listed[:memberListCap]
	}
	s.Summary = fmt.Sprintf("%d more %s %s alert(s) %s within %s of %s/%s: %s%s",
		n, f.Kind, f.Reason, verb, g.window, f.Namespace, f.Name, strings.Join(listed, ", "), suffix)

	s.Details["Grouped Resources"] = strings.Join(b.members, "\n")

	g.flush(s)
}
