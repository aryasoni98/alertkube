package azure

import (
	"context"
	"testing"

	"alertkube/internal/alert"
)

type fakeSQLLister struct {
	dbs []sqlDatabase
	err error
}

func (f *fakeSQLLister) List(context.Context) ([]sqlDatabase, error) { return f.dbs, f.err }

func sqlDB(server, name, location, status string) sqlDatabase {
	return sqlDatabase{server: server, name: name, location: location, status: status}
}

func TestEvaluateSQLDatabase(t *testing.T) {
	cases := []struct {
		name         string
		db           sqlDatabase
		wantEmit     bool
		wantResolved bool
	}{
		{"suspect critical", sqlDB("s", "d", "eastus", "Suspect"), true, false},
		{"offline critical", sqlDB("s", "d", "eastus", "Offline"), true, false},
		{"inaccessible critical", sqlDB("s", "d", "eastus", "Inaccessible"), true, false},
		{"emergency critical", sqlDB("s", "d", "eastus", "EmergencyMode"), true, false},
		{"shutdown critical", sqlDB("s", "d", "eastus", "Shutdown"), true, false},
		{"online resolves", sqlDB("s", "d", "eastus", "Online"), true, true},
		{"paused resolves", sqlDB("s", "d", "eastus", "Paused"), true, true},
		{"restoring resolves", sqlDB("s", "d", "eastus", "Restoring"), true, true},
		{"empty name skipped", sqlDB("s", "", "eastus", "Suspect"), false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			emit, got := collect()
			evaluateSQLDatabase("sub-1", tc.db, emit)
			if !tc.wantEmit {
				if len(*got) != 0 {
					t.Fatalf("expected no emit, got %d", len(*got))
				}
				return
			}
			if len(*got) != 1 {
				t.Fatalf("expected 1 alert, got %d", len(*got))
			}
			a := (*got)[0]
			if a.Kind != alert.KindAzureSQLDatabase || a.Namespace != "sub-1/eastus" || a.Name != "s/d" {
				t.Errorf("identity: kind=%s ns=%s name=%s", a.Kind, a.Namespace, a.Name)
			}
			if a.Resolved != tc.wantResolved {
				t.Fatalf("resolved = %v, want %v", a.Resolved, tc.wantResolved)
			}
			if !tc.wantResolved && a.Severity != alert.SeverityCritical {
				t.Errorf("severity = %q, want critical", a.Severity)
			}
		})
	}
}

func TestResourceGroupFromID(t *testing.T) {
	cases := map[string]string{
		"/subscriptions/sub/resourceGroups/my-rg/providers/Microsoft.Sql/servers/srv": "my-rg",
		"/subscriptions/sub/resourcegroups/lower-rg/providers/x/y/z":                  "lower-rg",
		"/malformed/path":                 "",
		"":                                "",
		"/subscriptions/s/resourceGroups": "",
	}
	for id, want := range cases {
		if got := resourceGroupFromID(id); got != want {
			t.Errorf("resourceGroupFromID(%q) = %q, want %q", id, got, want)
		}
	}
}

func TestAzureSQLSourcePoll(t *testing.T) {
	fake := &fakeSQLLister{dbs: []sqlDatabase{
		sqlDB("srv", "good", "eastus", "Online"),
		sqlDB("srv", "bad", "eastus", "Suspect"),
	}}
	src := &azureSQLSource{subs: []azureSQLSubscription{{subscription: "sub-1", lister: fake}}}
	emit, got := collect()
	src.Poll(context.Background(), emit)
	if len(*got) != 2 {
		t.Fatalf("expected 2 alerts, got %d", len(*got))
	}
}
