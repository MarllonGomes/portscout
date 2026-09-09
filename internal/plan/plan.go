// Package plan turns discovered remote ports into concrete local port
// assignments, resolving collisions against the local machine.
package plan

import (
	"fmt"
	"net"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// Assignment is one remote port paired with the local port to bind it to.
type Assignment struct {
	RemotePort int
	LocalPort  int
	Alias      string
	Remapped   bool // true when LocalPort != RemotePort because of a conflict
}

// LocalPortFree reports whether a TCP port can be bound on this machine.
func LocalPortFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// Build assigns a local port to every discovered port. The default is the
// remote port itself, which is what the user asked for; when that port is
// already used — by another entry in the config, by another assignment in this
// same run, or by something listening on this machine — the next free port is
// chosen and the assignment is flagged so the caller can say so out loud.
func Build(ports []discover.Port, taken map[int]bool, free func(int) bool) []Assignment {
	used := map[int]bool{}
	for p := range taken {
		used[p] = true
	}

	out := make([]Assignment, 0, len(ports))
	for _, p := range ports {
		a := Assignment{RemotePort: p.Port, LocalPort: p.Port, Alias: alias(p)}
		if used[p.Port] || !free(p.Port) {
			a.LocalPort = nextFree(p.Port, used, free)
			a.Remapped = true
		}
		used[a.LocalPort] = true
		out = append(out, a)
	}
	return out
}

// nextFree walks upward from the wanted port, staying in the unprivileged range
// and giving up rather than looping forever.
func nextFree(from int, used map[int]bool, free func(int) bool) int {
	for p := from + 1; p < 65536; p++ {
		if p < 1024 {
			continue
		}
		if !used[p] && free(p) {
			return p
		}
	}
	return 0
}

func alias(p discover.Port) string {
	if l := p.Label(); l != "" {
		return l
	}
	return fmt.Sprintf("port %d", p.Port)
}
