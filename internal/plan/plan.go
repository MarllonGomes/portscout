// Package plan turns discovered remote ports into local port assignments and
// into the names shown for them.
package plan

import (
	"fmt"
	"net"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// LocalPortFree reports whether a TCP port can be bound on this machine.
func LocalPortFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// Assign picks the local port for one newly discovered remote port. The default
// is the remote number itself, which is what the user means; when that is spoken
// for — by another row, or by something already listening here — it walks upward
// and reports the remap so the caller can say so out loud. A zero result means
// no port was available at all.
//
// This is deliberately per-port rather than per-scan. A row's local port is
// decided once, when the row first appears, and then frozen: recomputing the
// whole set on every rescan would see the ports portscout itself has bound as
// busy and move running tunnels out from under the user.
func Assign(want int, taken map[int]bool, free func(int) bool) (local int, remapped bool) {
	if want >= 1024 && !taken[want] && free(want) {
		return want, false
	}
	return nextFree(want, taken, free), true
}

// nextFree walks upward from the wanted port, staying in the unprivileged range
// and giving up rather than looping forever.
func nextFree(from int, taken map[int]bool, free func(int) bool) int {
	for p := from + 1; p < 65536; p++ {
		if p < 1024 {
			continue
		}
		if !taken[p] && free(p) {
			return p
		}
	}
	return 0
}

// Aliases resolves the display name of every discovered port. It is the single
// source of truth for naming: `list` has to print exactly what the dashboard
// shows, or the user picks a row they never saw in the listing.
//
// A container that publishes more than one port reports the same name for each,
// which would put identical rows in the list. Only the names that actually
// repeat get the port suffix, so the common case stays clean.
func Aliases(ports []discover.Port) map[int]string {
	count := map[string]int{}
	for _, p := range ports {
		count[alias(p)]++
	}
	names := make(map[int]string, len(ports))
	for _, p := range ports {
		name := alias(p)
		if count[name] > 1 {
			name = fmt.Sprintf("%s (%d)", name, p.Port)
		}
		names[p.Port] = name
	}
	return names
}

func alias(p discover.Port) string {
	if l := p.Label(); l != "" {
		return l
	}
	return fmt.Sprintf("port %d", p.Port)
}
