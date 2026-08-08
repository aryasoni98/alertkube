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
