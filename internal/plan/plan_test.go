package plan

import "testing"

func allFree(int) bool { return true }

func TestAssignDefaultsLocalToRemote(t *testing.T) {
	local, remapped := Assign(3100, map[int]bool{}, allFree)
	if local != 3100 {
		t.Errorf("local = %d, want 3100: the remote number is what the user means", local)
	}
	if remapped {
		t.Error("nothing moved, so nothing should be flagged as remapped")
	}
}

func TestAssignRemapsWhenTheLocalPortIsBusy(t *testing.T) {
	busy := func(p int) bool { return p != 5432 }
	local, remapped := Assign(5432, map[int]bool{}, busy)

	if local == 5432 {
		t.Fatal("must not assign a busy local port")
	}
	if !remapped {
		t.Error("a remap must be flagged so the caller can say so out loud")
	}
}

func TestAssignAvoidsPortsTakenByOtherRows(t *testing.T) {
	local, remapped := Assign(3100, map[int]bool{3100: true}, allFree)
	if local == 3100 {
		t.Fatal("3100 already belongs to another row")
	}
	if !remapped {
		t.Error("expected remapped to be true")
	}
}

// The caller accumulates `taken` as it assigns, which is what keeps two rows in
// one scan off the same local port.
func TestAssignDoesNotReuseAPortTheCallerAlreadyHandedOut(t *testing.T) {
	busy := func(p int) bool { return p != 4001 }
	taken := map[int]bool{}

	first, _ := Assign(4000, taken, busy)
	taken[first] = true
	second, _ := Assign(4001, taken, busy)

	if first == second {
		t.Fatalf("two rows collided on %d", first)
	}
}

func TestAssignStaysOutOfThePrivilegedRange(t *testing.T) {
	local, _ := Assign(80, map[int]bool{80: true}, allFree)
	if local != 0 && local < 1024 {
		t.Errorf("local = %d: a port under 1024 needs root, so it is never handed out", local)
	}
}

func TestAssignReportsZeroWhenNothingIsFree(t *testing.T) {
	local, _ := Assign(65530, map[int]bool{}, func(int) bool { return false })
	if local != 0 {
		t.Errorf("local = %d, want 0 to mean no port was available", local)
	}
}
