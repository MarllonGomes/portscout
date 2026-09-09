package plan

import (
	"testing"

	"github.com/MarllonGomes/portscout/internal/discover"
)

func allFree(int) bool { return true }

func TestBuildDefaultsLocalToRemote(t *testing.T) {
	ports := []discover.Port{
		{Port: 3100, Process: "next-server (v1"},
		{Port: 3102, Process: "rootlesskit", Container: "postgres-1"},
	}
	got := Build(ports, map[int]bool{}, allFree)

	if len(got) != 2 {
		t.Fatalf("got %d assignments, want 2", len(got))
	}
	if got[0].LocalPort != 3100 || got[0].Remapped {
		t.Errorf("3100 should map to itself: %+v", got[0])
	}
	if got[0].Alias != "next-server (v1" {
		t.Errorf("alias from process: %+v", got[0])
	}
	if got[1].Alias != "postgres-1" {
		t.Errorf("alias must prefer the container name: %+v", got[1])
	}
}

func TestBuildRemapsWhenLocalPortIsBusy(t *testing.T) {
	ports := []discover.Port{{Port: 5432, Process: "rootlesskit", Container: "pg"}}
	busy := func(p int) bool { return p != 5432 }

	got := Build(ports, map[int]bool{}, busy)

	if len(got) != 1 {
		t.Fatalf("got %d assignments", len(got))
	}
	if got[0].LocalPort == 5432 {
		t.Fatal("must not assign a busy local port")
	}
	if !got[0].Remapped {
		t.Error("a remap must be flagged so the caller can report it")
	}
	if got[0].RemotePort != 5432 {
		t.Errorf("remote port must not change: %+v", got[0])
	}
}

func TestBuildAvoidsPortsTakenByOtherEntries(t *testing.T) {
	ports := []discover.Port{{Port: 3100, Process: "app"}}
	got := Build(ports, map[int]bool{3100: true}, allFree)

	if got[0].LocalPort == 3100 {
		t.Fatal("3100 is already used by another entry in the file")
	}
	if !got[0].Remapped {
		t.Error("expected Remapped to be true")
	}
}

func TestBuildDoesNotReuseAPortWithinOneRun(t *testing.T) {
	ports := []discover.Port{{Port: 4000, Process: "a"}, {Port: 4001, Process: "b"}}
	busy := func(p int) bool { return p != 4001 }
	got := Build(ports, map[int]bool{}, busy)

	if got[0].LocalPort == got[1].LocalPort {
		t.Fatalf("two assignments collided: %+v", got)
	}
}

func TestBuildSkipsPortsWithoutLabel(t *testing.T) {
	got := Build([]discover.Port{{Port: 9999}}, map[int]bool{}, allFree)
	if len(got) != 1 {
		t.Fatalf("an unlabelled port is still forwardable: %+v", got)
	}
	if got[0].Alias != "port 9999" {
		t.Errorf("expected a fallback alias, got %q", got[0].Alias)
	}
}
