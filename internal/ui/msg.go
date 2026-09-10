package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/MarllonGomes/portscout/internal/session"
)

// snapshotMsg carries a fresh read of the backend. The UI never mutates rows on
// its own: every visible change arrives this way, so what is on screen is always
// something the session actually believes.
type snapshotMsg session.Snapshot

// doorbellMsg is the session's Changed channel firing. It carries nothing on
// purpose — the answer to a ring is always a full re-read.
type doorbellMsg struct{}

// backendGoneMsg is a closed Changed channel. Without it, a closed channel makes
// the wait command return instantly forever and spins a core.
type backendGoneMsg struct{}

// tickMsg drives the "varredura há Ns" clock, expires the status line, and acts
// as the safety net that re-reads the snapshot even if the doorbell misfires.
// That is what makes the doorbell a latency optimisation rather than a
// correctness requirement: at worst the screen is one second stale.
type tickMsg time.Time

type statusMsg struct {
	text string
	kind statusKind
}

// waitForChange blocks in its own goroutine, never in Update, and turns the next
// ring into a message. Update re-arms it on every ring.
func waitForChange(ch <-chan struct{}) tea.Cmd {
	return func() tea.Msg {
		if _, ok := <-ch; !ok {
			return backendGoneMsg{}
		}
		return doorbellMsg{}
	}
}

// readSnapshot is a command rather than an inline call so that Update contains
// no backend reads at all — a property that stays checkable by grep.
func readSnapshot(b Backend) tea.Cmd {
	return func() tea.Msg { return snapshotMsg(b.Snapshot()) }
}

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}
