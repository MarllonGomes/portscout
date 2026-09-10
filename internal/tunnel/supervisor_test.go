package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MarllonGomes/portscout/internal/sshmux"
)

const settle = 2 * time.Second

func newTestSupervisor(t *testing.T, c *fakeClient, clock *fakeClock) (*Supervisor, context.CancelFunc) {
	t.Helper()
	cfg := Config{Client: c}
	if clock != nil {
		cfg.After = clock.After
	}
	s := NewSupervisor(cfg)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = s.Run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(settle):
			t.Error("Run did not return after the context was cancelled")
		}
	})
	return s, cancel
}

// waitFor drains events until one satisfies match, or the deadline passes.
func waitFor(t *testing.T, s *Supervisor, match func(Event) bool) Event {
	t.Helper()
	deadline := time.After(settle)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				t.Fatal("event channel closed while waiting")
			}
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("timed out waiting for an event")
		}
	}
}

// eventually polls for a condition that is reached by a goroutine the test does
// not control, such as the ssh call a command dispatches.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func portState(port int, want State) func(Event) bool {
	return func(ev Event) bool {
		return ev.Kind == PortEvent && ev.Port == port && ev.State == want
	}
}

func TestStartBringsARowUp(t *testing.T) {
	c := newFakeClient()
	s, _ := newTestSupervisor(t, c, nil)

	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })
	s.Start(sshmux.Forward{LocalPort: 3100, RemotePort: 3100})

	waitFor(t, s, portState(3100, Up))
	if _, _, forwards, _ := c.counts(); len(forwards) != 1 || forwards[0] != 3100 {
		t.Errorf("forwards = %v, want exactly [3100]", forwards)
	}
}

func TestStopCancelsTheForward(t *testing.T) {
	c := newFakeClient()
	s, _ := newTestSupervisor(t, c, nil)
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	s.Start(sshmux.Forward{LocalPort: 3100, RemotePort: 3100})
	waitFor(t, s, portState(3100, Up))
	s.Stop(3100)
	waitFor(t, s, portState(3100, Off))

	// The row reports Off as soon as the user's intent changes; the ssh call
	// that tears the listener down happens off the loop, so wait for its effect.
	eventually(t, "the forward to be cancelled", func() bool {
		_, _, _, cancels := c.counts()
		return len(cancels) == 1 && cancels[0] == 3100
	})
}

// A busy local port does not free itself. Retrying it on a timer would produce a
// stream of identical failures and bury the one message the user has to read.
func TestBindFailureDoesNotRetry(t *testing.T) {
	c := newFakeClient()
	c.setForwardErr(3100, sshmux.Classify("forward", "cannot listen to port: 3100", errors.New("exit 255")))
	clock := newFakeClock()
	s, _ := newTestSupervisor(t, c, clock)
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	s.Start(sshmux.Forward{LocalPort: 3100, RemotePort: 3100})
	ev := waitFor(t, s, portState(3100, Failed))

	if ev.Err == nil || ev.Err.Kind != sshmux.KindBind {
		t.Fatalf("expected a classified bind error, got %+v", ev.Err)
	}
	if d, ok := clock.waitTimer(300 * time.Millisecond); ok {
		t.Errorf("a bind failure scheduled a retry in %v; it must not retry", d)
	}
	if _, _, forwards, _ := c.counts(); len(forwards) != 1 {
		t.Errorf("forwards = %v, want a single attempt", forwards)
	}
}

// The user pressed space twice before the first forward came back. The stale
// result must not resurrect a row they already turned off.
func TestStaleResultIsDiscarded(t *testing.T) {
	c := newFakeClient()
	c.forwardCh = make(chan int) // block inside Forward until the test releases it
	s, _ := newTestSupervisor(t, c, nil)
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	f := sshmux.Forward{LocalPort: 3100, RemotePort: 3100}
	s.Start(f)
	<-c.forwardCh // the forward is now in flight
	s.Stop(3100)  // ...and the user changed their mind

	waitFor(t, s, portState(3100, Off))

	// Nothing may flip it back to Up once the in-flight forward lands.
	select {
	case ev := <-s.Events():
		if ev.Kind == PortEvent && ev.Port == 3100 && ev.State == Up {
			t.Fatal("a stale forward result brought the row back up")
		}
	case <-time.After(300 * time.Millisecond):
	}
}

// A dropped link is not the user's fault and not their problem to fix: their
// choice stays intact and the rows go back to connecting while it heals.
func TestMasterDeathKeepsWantedRowsAndReplaysThem(t *testing.T) {
	c := newFakeClient()
	clock := newFakeClock()
	s, _ := newTestSupervisor(t, c, clock)
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	s.Start(sshmux.Forward{LocalPort: 3100, RemotePort: 3100})
	waitFor(t, s, portState(3100, Up))

	c.killMaster(errors.New("connection closed"))

	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkDown })
	waitFor(t, s, portState(3100, Connecting))

	if _, ok := clock.waitTimer(settle); !ok {
		t.Fatal("no reconnect was scheduled")
	}
	clock.fire()

	waitFor(t, s, portState(3100, Up))
	starts, _, forwards, _ := c.counts()
	if starts != 2 {
		t.Errorf("starts = %d, want 2 (the reconnect)", starts)
	}
	if len(forwards) != 2 {
		t.Errorf("forwards = %v, want the wanted row replayed after reconnect", forwards)
	}
}

func TestBackoffGrowsAndResetsOnSuccess(t *testing.T) {
	c := newFakeClient()
	c.startErr = errors.New("no route to host")
	clock := newFakeClock()
	s, _ := newTestSupervisor(t, c, clock)

	// Three failed dials, each scheduling a longer wait than the last.
	for i := 0; i < 3; i++ {
		if _, ok := clock.waitTimer(settle); !ok {
			t.Fatalf("no retry scheduled after failure %d", i+1)
		}
		clock.fire()
	}
	grew := clock.durations()
	if len(grew) < 3 {
		t.Fatalf("durations = %v, want at least 3", grew)
	}
	for i := 1; i < 3; i++ {
		if grew[i] <= grew[i-1] {
			t.Fatalf("backoff must grow: %v", grew)
		}
	}

	// Consume the pending retry before curing the host: firing it first would
	// race the dial against the assignment below.
	if _, ok := clock.waitTimer(settle); !ok {
		t.Fatal("no retry scheduled before recovery")
	}

	// The host comes back. A later failure must start over from the shortest
	// wait, or one bad afternoon would leave every reconnect 30s slow forever.
	c.mu.Lock()
	c.startErr = nil
	c.mu.Unlock()
	clock.fire()
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	c.killMaster(errors.New("dropped again"))
	next, ok := clock.waitTimer(settle)
	if !ok {
		t.Fatal("no retry scheduled after the link dropped again")
	}
	if next != grew[0] {
		t.Errorf("backoff after a success = %v, want it reset to %v", next, grew[0])
	}
}

// Hammering a refused key looks like an attack to sshd, and no amount of
// retrying makes a changed host key correct.
func TestAuthFailureIsFatalAndStopsRetrying(t *testing.T) {
	c := newFakeClient()
	c.startErr = sshmux.Classify("master", "Permission denied (publickey).", errors.New("exit 255"))
	clock := newFakeClock()
	s, _ := newTestSupervisor(t, c, clock)

	ev := waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkDown })
	if !ev.Fatal {
		t.Error("an auth failure must be reported as fatal")
	}
	if d, ok := clock.waitTimer(300 * time.Millisecond); ok {
		t.Errorf("scheduled a retry in %v after a fatal error", d)
	}
}

func TestCloseStopsTheClientAndClosesEvents(t *testing.T) {
	c := newFakeClient()
	s, cancel := newTestSupervisor(t, c, nil)
	waitFor(t, s, func(ev Event) bool { return ev.Kind == LinkEvent && ev.Link == LinkUp })

	cancel()

	deadline := time.After(settle)
	for {
		select {
		case _, ok := <-s.Events():
			if !ok {
				if _, stops, _, _ := c.counts(); stops == 0 {
					t.Error("the ssh master was never stopped")
				}
				return
			}
		case <-deadline:
			t.Fatal("the event channel was never closed")
		}
	}
}

// A consumer that is mid-render must never be able to stall the tunnels.
func TestEventsAreDroppedRatherThanBlockingTheLoop(t *testing.T) {
	c := newFakeClient()
	s := NewSupervisor(Config{Client: c, Buffer: 1})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Run(ctx) }()

	// Nobody ever reads s.Events(); the loop must still accept commands.
	deadline := time.After(settle)
	for i := 0; i < 50; i++ {
		done := make(chan struct{})
		go func(port int) {
			s.Start(sshmux.Forward{LocalPort: port, RemotePort: port})
			close(done)
		}(3100 + i)
		select {
		case <-done:
		case <-deadline:
			t.Fatalf("Start blocked at iteration %d with a full event buffer", i)
		}
	}
}
