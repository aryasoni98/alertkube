package alert

// Correlation is derived, non-persisted context attached to an active alert by
// the correlation engine (internal/correlate). Nil when correlation is disabled,
// or the alert stands alone. It is recomputed each interval and never written to
// the persisted Snapshot, so it must not influence dedupe/fingerprint state.
type Correlation struct {
	GroupID     string
	Role        string
	RootFP      string
	Confidence  float64
	BlastRadius []Ref
}

const (
	RoleCause      = "cause"
	RoleEffect     = "effect"
	RoleStandalone = "standalone"
)

// Ref identifies a Kubernetes object in a blast radius (alerting or not).
type Ref struct {
	Kind     string
	Name     string
	Alerting bool
}

// clone returns an independent copy (nil stays nil), so the store can hand out
// copies whose BlastRadius slice is not shared with the live alert.
func (c *Correlation) clone() *Correlation {
	if c == nil {
		return nil
	}
	cp := *c
	cp.BlastRadius = make([]Ref, len(c.BlastRadius))
	copy(cp.BlastRadius, c.BlastRadius)
	return &cp
}
