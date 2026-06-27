package config

import (
	"testing"
	"time"
)

func mustTime(t *testing.T, rfc string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, rfc)
	if err != nil {
		t.Fatalf("parse %q: %v", rfc, err)
	}
	return ts
}

func TestMaintenanceActiveSameDay(t *testing.T) {
	w := MaintenanceWindow{Matchers: map[string]string{"namespace": "prod"}, Start: "01:00", End: "05:00"}
	// 02:30 UTC -> active.
	if !w.Active(mustTime(t, "2026-01-02T02:30:00Z")) {
		t.Fatal("02:30 should be inside 01:00-05:00")
	}
	// 05:00 is exclusive end -> not active.
	if w.Active(mustTime(t, "2026-01-02T05:00:00Z")) {
		t.Fatal("05:00 should be outside (end-exclusive)")
	}
	// 00:30 before start -> not active.
	if w.Active(mustTime(t, "2026-01-02T00:30:00Z")) {
		t.Fatal("00:30 should be outside")
	}
	// 01:00 inclusive start -> active.
	if !w.Active(mustTime(t, "2026-01-02T01:00:00Z")) {
		t.Fatal("01:00 should be inside (start-inclusive)")
	}
}

func TestMaintenanceActiveWrapsMidnight(t *testing.T) {
	w := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "23:00", End: "02:00"}
	if !w.Active(mustTime(t, "2026-01-02T23:30:00Z")) {
		t.Fatal("23:30 should be inside a 23:00-02:00 wrap window")
	}
	if !w.Active(mustTime(t, "2026-01-03T01:30:00Z")) {
		t.Fatal("01:30 next day should still be inside the wrap window")
	}
	if w.Active(mustTime(t, "2026-01-03T02:30:00Z")) {
		t.Fatal("02:30 should be outside the wrap window")
	}
	if w.Active(mustTime(t, "2026-01-02T12:00:00Z")) {
		t.Fatal("midday should be outside the wrap window")
	}
}

func TestMaintenanceDayRestriction(t *testing.T) {
	// 2026-01-02 is a Friday.
	w := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "05:00", Days: []string{"sat", "sun"}}
	if w.Active(mustTime(t, "2026-01-02T02:00:00Z")) {
		t.Fatal("Friday must be excluded when Days is sat,sun")
	}
	// 2026-01-03 is a Saturday.
	if !w.Active(mustTime(t, "2026-01-03T02:00:00Z")) {
		t.Fatal("Saturday must be included")
	}
}

func TestMaintenanceWrapDayRestrictionUsesStartDay(t *testing.T) {
	// Window Fri 23:00 -> Sat 02:00, restricted to Fridays. The Saturday-morning
	// portion belongs to Friday's window start, so it must still be active.
	w := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "23:00", End: "02:00", Days: []string{"fri"}}
	// 2026-01-02 Fri 23:30 -> active.
	if !w.Active(mustTime(t, "2026-01-02T23:30:00Z")) {
		t.Fatal("Friday 23:30 should be active")
	}
	// 2026-01-03 Sat 01:00 -> belongs to Friday's window -> active.
	if !w.Active(mustTime(t, "2026-01-03T01:00:00Z")) {
		t.Fatal("Saturday 01:00 (Friday's window tail) should be active")
	}
	// 2026-01-03 Sat 23:30 -> Saturday is not allowed -> inactive.
	if w.Active(mustTime(t, "2026-01-03T23:30:00Z")) {
		t.Fatal("Saturday 23:30 should be inactive (only fri allowed)")
	}
}

func TestMaintenanceTimezone(t *testing.T) {
	w := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "22:00", End: "23:00", Timezone: "America/New_York"}
	// 22:30 America/New_York on 2026-01-02 is 03:30 UTC on 2026-01-03 (EST = UTC-5).
	if !w.Active(mustTime(t, "2026-01-03T03:30:00Z")) {
		t.Fatal("should be active at the NY-local 22:30")
	}
	// Same UTC instant interpreted as UTC would be 03:30, well outside 22:00-23:00.
	wUTC := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "22:00", End: "23:00"}
	if wUTC.Active(mustTime(t, "2026-01-03T03:30:00Z")) {
		t.Fatal("UTC window must not be active at 03:30 UTC")
	}
}

func TestMaintenanceEmptyWindowSuppressesNothing(t *testing.T) {
	w := MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "03:00", End: "03:00"}
	if w.Active(mustTime(t, "2026-01-02T03:00:00Z")) {
		t.Fatal("start==end is an empty window and must never be active")
	}
}

func TestMaintenanceValidate(t *testing.T) {
	cases := []struct {
		name string
		w    MaintenanceWindow
		ok   bool
	}{
		{"valid", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "05:00"}, true},
		{"valid with tz/days", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "05:00", Days: []string{"Mon"}, Timezone: "UTC"}, true},
		{"no matchers", MaintenanceWindow{Start: "01:00", End: "05:00"}, false},
		{"bad start", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "25:00", End: "05:00"}, false},
		{"bad end", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "5pm"}, false},
		{"bad day", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "05:00", Days: []string{"funday"}}, false},
		{"bad tz", MaintenanceWindow{Matchers: map[string]string{"x": "y"}, Start: "01:00", End: "05:00", Timezone: "Mars/Phobos"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := c.w.validate()
			if c.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !c.ok && err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

func TestConfigValidateMaintenance(t *testing.T) {
	raw := []byte(`
cluster: test
behavior: {muteSeconds: 600, resolveTTLSeconds: 600, pvcPendingSeconds: 300}
maintenance:
  - name: nightly-backup
    matchers: {namespace: prod}
    start: "23:00"
    end: "02:00"
    days: [mon, tue]
    timezone: UTC
`)
	if err := ParseAndValidate(raw); err != nil {
		t.Fatalf("valid maintenance config rejected: %v", err)
	}

	bad := []byte(`
cluster: test
behavior: {muteSeconds: 600, resolveTTLSeconds: 600, pvcPendingSeconds: 300}
maintenance:
  - matchers: {namespace: prod}
    start: "99:00"
    end: "02:00"
`)
	if err := ParseAndValidate(bad); err == nil {
		t.Fatal("invalid maintenance window must be rejected at load")
	}
}
