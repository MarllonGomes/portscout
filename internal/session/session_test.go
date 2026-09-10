package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/sshmux"
	"github.com/MarllonGomes/portscout/internal/state"
	"github.com/MarllonGomes/portscout/internal/tunnel"
)

const settle = 2 * time.Second

// fakeForwarder records what the session asked for and lets a test push events
// back, standing in for the whole ssh supervisor.
type fakeForwarder struct {
	events  chan tunnel.Event
	started chan sshmux.Forward
	stopped chan int
}

func newFakeForwarder() *fakeForwarder {
	return &fakeForwarder{
		events:  make(chan tunnel.Event, 32),
		started: make(chan sshmux.Forward, 32),
		stopped: make(chan int, 32),
	}
}

func (f *fakeForwarder) Start(fwd sshmux.Forward)    { f.started <- fwd }
func (f *fakeForwarder) Stop(port int)               { f.stopped <- port }
func (f *fakeForwarder) Events() <-chan tunnel.Event { return f.events }
func (f *fakeForwarder) linkUp() {
	f.events <- tunnel.Event{Kind: tunnel.LinkEvent, Link: tunnel.LinkUp}
}

type fakeRunner struct {
	out string
	err error
}

func (f fakeRunner) Run(context.Context, string, string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	return []byte(f.out), nil
}

// remote builds the two-section payload the discovery script emits.
func remote(ssLines ...string) string {
	out := "portscout-section:ss\n"
	for _, l := range ssLines {
		out += l + "\n"
	}
	return out + "portscout-section:docker\n"
}

func ssLine(port int, proc string) string {
	return "LISTEN 0 511 127.0.0.1:" + itoa(port) + " 0.0.0.0:* users:((\"" + proc + "\",pid=1,fd=3))"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

type harness struct {
	s     *Session
	fwd   *fakeForwarder
	ticks chan time.Time
	store *state.Store
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	fwd := newFakeForwarder()
	ticks := make(chan time.Time, 4)

	cfg.Host = "dev"
	cfg.Forwarder = fwd
	cfg.Tick = ticks
	if cfg.PortFree == nil {
		cfg.PortFree = func(int) bool { return true }
	}
	if cfg.Store == nil {
		cfg.Store = state.OpenAt(filepath.Join(t.TempDir(), "dev.json"), "dev")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Unix(0, 0) }
	}
	if cfg.SaveDelay == 0 {
		cfg.SaveDelay = time.Millisecond
	}

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
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
			t.Error("Run did not return")
		}
	})
	return &harness{s: s, fwd: fwd, ticks: ticks, store: cfg.Store}
}

// waitSnap polls until the snapshot satisfies cond.
func (h *harness) waitSnap(t *testing.T, what string, cond func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if snap := h.s.Snapshot(); cond(snap) {
			return snap
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; snapshot = %+v", what, h.s.Snapshot())
	return Snapshot{}
}

func rowFor(snap Snapshot, port int) (Row, bool) {
	for _, r := range snap.Rows {
		if r.RemotePort == port {
			return r, true
		}
	}
	return Row{}, false
}

func TestScanCreatesRows(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "next-server"), ssLine(3102, "api"))}})
	h.fwd.linkUp()

	snap := h.waitSnap(t, "two rows", func(s Snapshot) bool { return len(s.Rows) == 2 })
	r, _ := rowFor(snap, 3100)
	if r.LocalPort != 3100 || r.Alias != "next-server" || !r.Present {
		t.Errorf("row = %+v", r)
	}
	if r.Wanted {
		t.Error("a freshly discovered row must start off")
	}
}

// The single most important rule in the program: one flaky ssh call must never
// cost the user a row, a checkbox or a running tunnel.
func TestAFailedScanChangesNothing(t *testing.T) {
	runner := &switchableRunner{out: remote(ssLine(3100, "next-server"))}
	h := newHarness(t, Config{Runner: runner})
	h.fwd.linkUp()

	h.waitSnap(t, "the first scan", func(s Snapshot) bool { return len(s.Rows) == 1 })
	h.s.Toggle(3100)
	<-h.fwd.started
	h.fwd.events <- tunnel.Event{Kind: tunnel.PortEvent, Port: 3100, State: tunnel.Up}
	h.waitSnap(t, "the tunnel up", func(s Snapshot) bool {
		r, ok := rowFor(s, 3100)
		return ok && r.State == tunnel.Up
	})

	runner.fail(errors.New("ssh: connect to host dev port 22: Connection refused"))
	h.ticks <- time.Now()

	snap := h.waitSnap(t, "the scan error", func(s Snapshot) bool { return s.ScanErr != "" })
	r, ok := rowFor(snap, 3100)
	if !ok {
		t.Fatal("a failed scan removed the row")
	}
	if !r.Wanted {
		t.Error("a failed scan dropped the user's choice")
	}
	if r.State != tunnel.Up {
		t.Errorf("a failed scan disturbed the tunnel: state = %v", r.State)
	}
	if !r.Present {
		t.Error("a failed scan must not mark rows absent: it learned nothing")
	}
}

// A dev server restarting for three seconds must not cost the user anything.
func TestAVanishedPortKeepsItsRowAndItsTunnel(t *testing.T) {
	runner := &switchableRunner{out: remote(ssLine(3100, "next-server"))}
	h := newHarness(t, Config{Runner: runner})
	h.fwd.linkUp()
	h.waitSnap(t, "the first scan", func(s Snapshot) bool { return len(s.Rows) == 1 })

	h.s.Toggle(3100)
	<-h.fwd.started
	h.fwd.events <- tunnel.Event{Kind: tunnel.PortEvent, Port: 3100, State: tunnel.Up}

	runner.set(remote()) // the port is gone from the remote
	h.ticks <- time.Now()

	snap := h.waitSnap(t, "the row to be marked absent", func(s Snapshot) bool {
		r, ok := rowFor(s, 3100)
		return ok && !r.Present
	})
	r, _ := rowFor(snap, 3100)
	if !r.Wanted || r.State != tunnel.Up {
		t.Errorf("the tunnel is genuinely still open and must be reported as such: %+v", r)
	}
}

func TestToggleStartsAndStopsTheForward(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "app"))}})
	h.fwd.linkUp()
	h.waitSnap(t, "the row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	h.s.Toggle(3100)
	fwd := <-h.fwd.started
	if fwd.LocalPort != 3100 || fwd.RemotePort != 3100 {
		t.Errorf("forward = %+v", fwd)
	}

	h.s.Toggle(3100)
	if port := <-h.fwd.stopped; port != 3100 {
		t.Errorf("stopped %d, want 3100", port)
	}
}

// Silently remapping a number the user typed is what makes a tool feel
// dishonest, so a pin is verbatim and survives a restart.
func TestPinnedPortIsVerbatimAndPersists(t *testing.T) {
	dir := t.TempDir()
	store := state.OpenAt(filepath.Join(dir, "dev.json"), "dev")
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "app"))}, Store: store})
	h.fwd.linkUp()
	h.waitSnap(t, "the row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	if err := h.s.SetLocalPort(3100, 8080); err != nil {
		t.Fatalf("SetLocalPort: %v", err)
	}
	snap := h.waitSnap(t, "the pin", func(s Snapshot) bool {
		r, ok := rowFor(s, 3100)
		return ok && r.LocalPort == 8080
	})
	if r, _ := rowFor(snap, 3100); !r.Pinned {
		t.Error("the row must be flagged as pinned")
	}

	// A rescan must never move it.
	h.ticks <- time.Now()
	time.Sleep(50 * time.Millisecond)
	if r, _ := rowFor(h.s.Snapshot(), 3100); r.LocalPort != 8080 {
		t.Errorf("a rescan moved a pinned port to %d", r.LocalPort)
	}

	// And a new session must load it back, once the debounced save has landed.
	// The reader is its own Store: the session owns the one it writes through.
	reader := state.OpenAt(store.Path(), "dev")
	deadline := time.Now().Add(settle)
	for time.Now().Before(deadline) {
		if got, _ := reader.Load(); len(got.Choices) == 1 && got.Choices[0].LocalPort == 8080 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h2 := newHarness(t, Config{
		Runner: fakeRunner{out: remote(ssLine(3100, "app"))},
		Store:  state.OpenAt(store.Path(), "dev"),
	})
	if r, ok := rowFor(h2.s.Snapshot(), 3100); !ok || r.LocalPort != 8080 || !r.Pinned {
		t.Errorf("the pin did not survive a restart: %+v", r)
	}
}

func TestSetLocalPortRejectsCollisionsAndRange(t *testing.T) {
	h := newHarness(t, Config{
		Runner:   fakeRunner{out: remote(ssLine(3100, "app"), ssLine(3102, "db"))},
		PortFree: func(p int) bool { return p != 9999 },
	})
	h.fwd.linkUp()
	h.waitSnap(t, "two rows", func(s Snapshot) bool { return len(s.Rows) == 2 })

	if err := h.s.SetLocalPort(3100, 80); err == nil {
		t.Error("a privileged port needs root and must be refused up front")
	}
	if err := h.s.SetLocalPort(3100, 70000); err == nil {
		t.Error("a port above the range must be refused")
	}
	if err := h.s.SetLocalPort(3100, 3102); err == nil {
		t.Error("a port belonging to another row must be refused")
	}
	if err := h.s.SetLocalPort(3100, 9999); err == nil {
		t.Error("a port busy on this machine must be refused")
	}
	if r, _ := rowFor(h.s.Snapshot(), 3100); r.LocalPort != 3100 {
		t.Errorf("a refused edit must not change anything: %+v", r)
	}
}

// The dashboard has to paint a full table in the first frame, before any network
// I/O, or a slow handshake looks like a hung binary.
func TestRememberedRowsExistBeforeAnyScan(t *testing.T) {
	dir := t.TempDir()
	store := state.OpenAt(filepath.Join(dir, "dev.json"), "dev")
	if err := store.Save(state.Host{Host: "dev", Choices: []state.Choice{
		{RemotePort: 3100, LocalPort: 3102, Wanted: true, Alias: "next-server"},
	}}); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, Config{Store: store, AutoStart: true})
	snap := h.s.Snapshot() // no link, no scan, nothing has happened yet
	r, ok := rowFor(snap, 3100)
	if !ok {
		t.Fatal("a remembered row must be in the very first snapshot")
	}
	if r.LocalPort != 3102 || !r.Wanted || r.Alias != "next-server" {
		t.Errorf("row = %+v", r)
	}
}

func TestAutoStartRunsOnceTheLinkIsUp(t *testing.T) {
	store := state.OpenAt(filepath.Join(t.TempDir(), "dev.json"), "dev")
	_ = store.Save(state.Host{Host: "dev", Choices: []state.Choice{
		{RemotePort: 3100, LocalPort: 3102, Wanted: true},
	}})

	h := newHarness(t, Config{Store: store, AutoStart: true})
	select {
	case fwd := <-h.fwd.started:
		t.Fatalf("started %+v before the link was up", fwd)
	case <-time.After(50 * time.Millisecond):
	}

	h.fwd.linkUp()
	select {
	case fwd := <-h.fwd.started:
		if fwd.LocalPort != 3102 || fwd.RemotePort != 3100 {
			t.Errorf("forward = %+v", fwd)
		}
	case <-time.After(settle):
		t.Fatal("a remembered row was never auto-started")
	}
}

// A busy local port is the user's to resolve. The row must stop claiming it is
// wanted, or it would look like it is still trying.
func TestABindFailureClearsTheIntent(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "app"))}})
	h.fwd.linkUp()
	h.waitSnap(t, "the row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	h.s.Toggle(3100)
	<-h.fwd.started
	h.fwd.events <- tunnel.Event{
		Kind:  tunnel.PortEvent,
		Port:  3100,
		State: tunnel.Failed,
		Err:   sshmux.Classify("forward", "cannot listen to port: 3100", errors.New("exit 255")),
	}

	snap := h.waitSnap(t, "the failure", func(s Snapshot) bool {
		r, ok := rowFor(s, 3100)
		return ok && r.State == tunnel.Failed
	})
	r, _ := rowFor(snap, 3100)
	if r.Wanted {
		t.Error("a bind failure must clear the intent")
	}
	if r.Err == "" {
		t.Error("the user needs a message they can act on")
	}
	if r.Detail == "" {
		t.Error("ssh's own words must survive for the detail line")
	}
}

func TestNoisyPortsAreHiddenUntilAskedFor(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(22, "sshd"), ssLine(3100, "app"))}})
	h.fwd.linkUp()
	h.waitSnap(t, "the app row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	if _, ok := rowFor(h.s.Snapshot(), 22); ok {
		t.Error("sshd is noise and must be hidden by default")
	}

	h.s.SetShowAll(true)
	h.waitSnap(t, "everything", func(s Snapshot) bool { return len(s.Rows) == 2 })
}

func TestSetAllTogglesEveryVisibleRow(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "a"), ssLine(3102, "b"))}})
	h.fwd.linkUp()
	h.waitSnap(t, "two rows", func(s Snapshot) bool { return len(s.Rows) == 2 })

	h.s.SetAll(true)
	got := map[int]bool{}
	for i := 0; i < 2; i++ {
		select {
		case fwd := <-h.fwd.started:
			got[fwd.RemotePort] = true
		case <-time.After(settle):
			t.Fatalf("only %d rows were started", len(got))
		}
	}
	if !got[3100] || !got[3102] {
		t.Errorf("started = %v", got)
	}
}

// The doorbell is a hint, never the data: a consumer that never reads it must
// not be able to stall the session.
func TestChangedNeverBlocksTheSession(t *testing.T) {
	h := newHarness(t, Config{Runner: fakeRunner{out: remote(ssLine(3100, "app"))}})
	h.fwd.linkUp()
	h.waitSnap(t, "the row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	for i := 0; i < 100; i++ {
		h.s.SetShowAll(i%2 == 0)
	}
	h.waitSnap(t, "the session to still be alive", func(s Snapshot) bool { return s.Rev > 100 })
}

// switchableRunner lets a test change what the remote reports mid-flight.
type switchableRunner struct {
	mu  chan struct{}
	out string
	err error
}

func (r *switchableRunner) lock() {
	if r.mu == nil {
		r.mu = make(chan struct{}, 1)
	}
	r.mu <- struct{}{}
}
func (r *switchableRunner) unlock() { <-r.mu }

func (r *switchableRunner) set(out string) {
	r.lock()
	r.out, r.err = out, nil
	r.unlock()
}

func (r *switchableRunner) fail(err error) {
	r.lock()
	r.err = err
	r.unlock()
}

func (r *switchableRunner) Run(context.Context, string, string) ([]byte, error) {
	r.lock()
	defer r.unlock()
	if r.err != nil {
		return nil, r.err
	}
	return []byte(r.out), nil
}

var _ discover.Runner = (*switchableRunner)(nil)

// Every system port under 1024 is remapped by definition. Announcing those
// buries the one remap the user actually needs to see, about a row they can see.
func TestNoiseDoesNotRaiseARemapNotice(t *testing.T) {
	h := newHarness(t, Config{
		Runner: fakeRunner{out: remote(ssLine(53, "systemd-resolve"), ssLine(3100, "app"))},
	})
	h.fwd.linkUp()
	h.waitSnap(t, "the app row", func(s Snapshot) bool { return len(s.Rows) == 1 })

	if snap := h.s.Snapshot(); strings.Contains(snap.Notice, "53") {
		t.Errorf("a hidden noise port must not produce a notice: %q", snap.Notice)
	}
}

// A silent remap is worse than the collision, but six lines about it are worse
// than one.
func TestSeveralRemapsBecomeOneNotice(t *testing.T) {
	h := newHarness(t, Config{
		Runner:   fakeRunner{out: remote(ssLine(3100, "a"), ssLine(3101, "b"), ssLine(3102, "c"))},
		PortFree: func(p int) bool { return p != 3100 && p != 3101 && p != 3102 },
	})
	h.fwd.linkUp()
	snap := h.waitSnap(t, "the notice", func(s Snapshot) bool { return s.Notice != "" })

	if !strings.Contains(snap.Notice, "3 portas remapeadas") {
		t.Errorf("Notice = %q, want one summary line", snap.Notice)
	}
	if snap.NoticeSeq == 0 {
		t.Error("a notice must carry a sequence so the UI can latch it once")
	}
}
