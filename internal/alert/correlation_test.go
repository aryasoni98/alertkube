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

func TestCorrelationCloneNilBlastRadius(t *testing.T) {
	a := New(KindPod, "ns", "web-1", "X", SeverityInfo)
	a.Correlation = &Correlation{GroupID: "g1", Role: RoleCause}
	cp := a.Clone()
	if cp.Correlation.BlastRadius != nil {
		t.Fatal("nil BlastRadius must stay nil through clone")
	}
}
