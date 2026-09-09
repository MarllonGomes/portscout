package tunnel9

import (
	"os"
	"strings"
	"testing"

	"github.com/MarllonGomes/portscout/internal/plan"
)

func TestMergeAddsNewEntry(t *testing.T) {
	cfg, _ := loadFixture(t)
	changes := cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3100, LocalPort: 3100, Alias: "next-server"},
	}, "auto", false)

	if len(changes) != 1 || changes[0].Kind != Added {
		t.Fatalf("expected one Added change, got %+v", changes)
	}
	var found bool
	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3100 {
			found = true
			if e.LocalPort != 3100 || e.Tag != "auto" || e.Alias != "next-server" {
				t.Errorf("new entry wrong: %+v", e)
			}
		}
	}
	if !found {
		t.Error("new entry was not added")
	}
}

func TestMergePreservesUserEditedLocalPort(t *testing.T) {
	cfg, _ := loadFixture(t)
	cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3102, LocalPort: 3102, Alias: "postgres-novo"},
	}, "auto", false)

	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			if e.LocalPort != 4102 {
				t.Errorf("local_port must be preserved, got %d", e.LocalPort)
			}
			if e.Alias != "postgres-novo" {
				t.Errorf("alias should refresh, got %q", e.Alias)
			}
		}
	}
}

func TestMergeNeverTouchesUntaggedEntries(t *testing.T) {
	cfg, path := loadFixture(t)
	cfg.Merge("prod-db.example.com", []plan.Assignment{
		{RemotePort: 5432, LocalPort: 5432, Alias: "hijack"},
	}, "auto", true)

	for _, e := range cfg.Entries() {
		if e.Host == "prod-db.example.com" && e.Tag == "" {
			if e.LocalPort != 15432 || e.Alias != "prod-db" {
				t.Errorf("manual entry was modified: %+v", e)
			}
		}
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "15432") {
		t.Error("manual entry disappeared from the file")
	}
}

func TestMergePruneRemovesOnlyManagedGoneEntries(t *testing.T) {
	cfg, _ := loadFixture(t)
	changes := cfg.Merge("dev", nil, "auto", true)

	var pruned int
	for _, c := range changes {
		if c.Kind == Pruned {
			pruned++
		}
	}
	if pruned != 1 {
		t.Fatalf("expected 1 pruned entry, got %d (%+v)", pruned, changes)
	}
	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			t.Error("managed gone entry should have been pruned")
		}
	}
	for _, e := range cfg.Entries() {
		if e.Host == "prod-db.example.com" && e.Tag == "" {
			return
		}
	}
	t.Error("manual entry must survive prune")
}

func TestMergeWithoutPruneKeepsGoneEntries(t *testing.T) {
	cfg, _ := loadFixture(t)
	cfg.Merge("dev", nil, "auto", false)

	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			return
		}
	}
	t.Error("without --prune the entry must stay")
}

func TestTakenLocalPorts(t *testing.T) {
	cfg, _ := loadFixture(t)
	taken := cfg.TakenLocalPorts()
	if !taken[15432] || !taken[4102] {
		t.Errorf("expected both local ports reported as taken, got %+v", taken)
	}
}
