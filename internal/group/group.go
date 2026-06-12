// Package group folds alert storms: the first alert of a group passes
// through immediately (pages stay fast), subsequent alerts in the same
// group within the window are absorbed and surface as one summary alert
// when the window closes. 200 crashlooping pods become two messages
// instead of 200.
package group

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"alertkube/internal/alert"
)

// DefaultBy is the group identity when config does not override it.
var DefaultBy = []string{"kind", "namespace", "reason", "severity"}

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
}

type bucket struct {
	first    *alert.Alert
	members  []string
	deadline time.Time
}

// New builds a Grouper. flush receives each summary alert; it is invoked
// without the Grouper lock held.
func New(window time.Duration, by []string, flush func(*alert.Alert)) *Grouper {
	if len(by) == 0 {
		by = DefaultBy
	}
	return &Grouper{
		window:  window,
		by:      by,
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
	b, ok := g.buckets[key]
	if ok && now.After(b.deadline) {
		// Window closed but the flusher has not run yet: flush the old
		// bucket inline and let this alert open (and lead) a new window.
		delete(g.buckets, key)
		g.buckets[key] = &bucket{first: a, deadline: now.Add(g.window)}
		g.mu.Unlock()
		g.emitSummary(b)
		return true
	}
	if !ok {
		g.buckets[key] = &bucket{first: a, deadline: now.Add(g.window)}
		g.mu.Unlock()
		return true
	}
	b.members = append(b.members, a.Namespace+"/"+a.Name)
	g.mu.Unlock()
	return false
}

// Run flushes expired windows until ctx is cancelled, then drains every
// open bucket so absorbed alerts are not lost on shutdown.
func (g *Grouper) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			g.FlushAll()
			return
		case <-ticker.C:
			g.flushExpired(time.Now())
		}
	}
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
	for _, b := range expired {
		g.emitSummary(b)
	}
}

// FlushAll closes every open window immediately.
func (g *Grouper) FlushAll() {
	g.mu.Lock()
	var all []*bucket
	for key, b := range g.buckets {
		all = append(all, b)
		delete(g.buckets, key)
	}
	g.mu.Unlock()
	for _, b := range all {
		g.emitSummary(b)
	}
}

// emitSummary builds and flushes the summary alert for a bucket that
// absorbed at least one member. A bucket whose window passed with no
// absorptions produces nothing - the pass-through alert already told the
// whole story.
func (g *Grouper) emitSummary(b *bucket) {
	n := len(b.members)
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
		suffix = fmt.Sprintf(" (+%d more)", len(listed)-memberListCap)
		listed = listed[:memberListCap]
	}
	s.Summary = fmt.Sprintf("%d more %s %s alert(s) %s within %s of %s/%s: %s%s",
		n, f.Kind, f.Reason, verb, g.window, f.Namespace, f.Name, strings.Join(listed, ", "), suffix)

	detail := b.members
	if len(detail) > memberDetailCap {
		detail = detail[:memberDetailCap]
	}
	s.Details["Grouped Resources"] = strings.Join(detail, "\n")

	g.flush(s)
}
