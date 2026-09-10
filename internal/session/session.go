// Package session is the dashboard without a terminal: it owns the rows, the
// discovery loop and the persisted choices, and publishes an immutable snapshot
// for a UI to render.
//
// Keeping this headless is what makes the whole program testable. Every rule
// that matters — a failed scan changes nothing, a vanished port keeps its row,
// a pinned port is never remapped — is exercised here without a TTY and without
// opening ssh.
package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/plan"
	"github.com/MarllonGomes/portscout/internal/sshmux"
	"github.com/MarllonGomes/portscout/internal/state"
	"github.com/MarllonGomes/portscout/internal/tunnel"
)

// Row is one remote port and everything shown about it.
type Row struct {
	RemotePort int
	LocalPort  int
	Alias      string
	RemoteHost string
	Pinned     bool // the user typed this local port
	Wanted     bool // the user's intent, which survives a failure
	Present    bool // seen in the most recent successful scan
	Noisy      bool // infrastructure; hidden unless the user asks for everything
	State      tunnel.State
	Err        string // ready to render, empty when fine
	Detail     string // ssh's own words, for the detail line
}

// Snapshot is an immutable view of the whole session. It is never mutated after
// publication, so a UI can hold one across frames.
type Snapshot struct {
	Host     string
	Link     tunnel.Link
	LinkErr  string
	Fatal    bool
	Rows     []Row
	LastScan time.Time
	Scanning bool
	ScanErr  string
	Notice   string
	// NoticeSeq changes only when Notice is new. A consumer latches the text on
	// a new sequence, which is what keeps a one-off message from being
	// re-displayed forever by every later snapshot.
	NoticeSeq uint64
	Rev       uint64
}

// Forwarder is the slice of the supervisor a session needs; tests replace it.
type Forwarder interface {
	Start(f sshmux.Forward)
	Stop(remotePort int)
	Events() <-chan tunnel.Event
}

// Config wires a session. Everything that touches the outside world is a seam.
type Config struct {
	Host      string
	Forwarder Forwarder
	Runner    discover.Runner
	Store     *state.Store
	PortFree  func(int) bool
	Interval  time.Duration
	// SaveDelay debounces writes to the state file. A rescan refreshes aliases
	// constantly, and a keypress should not cost a disk write each time.
	SaveDelay time.Duration
	Tick      <-chan time.Time // tests drive rescans by hand
	Now       func() time.Time
	ShowAll   bool
	AutoStart bool
}

// Session owns the rows. All mutation happens on the Run goroutine.
type Session struct {
	cfg       Config
	now       func() time.Time
	interval  time.Duration
	saveDelay time.Duration

	snap    atomic.Pointer[Snapshot]
	changed chan struct{}

	inbox chan func()
	scans chan scanResult

	// owned by Run
	rows      map[int]*Row
	link      tunnel.Link
	linkErr   string
	fatal     bool
	scanning  bool
	lastScan  time.Time
	scanErr   string
	notice    string
	noticeSeq uint64
	rev       uint64
	dirty     bool
	scanReq   chan struct{}
}

type scanResult struct {
	ports []discover.Port
	err   error
}

const defaultInterval = 10 * time.Second

// New builds a session and loads the remembered choices, so the first snapshot
// already has rows in it — the dashboard paints a full table before any network
// I/O happens, instead of opening on a blank screen for two seconds.
func New(cfg Config) (*Session, error) {
	if cfg.Host == "" {
		return nil, errors.New("session: host vazio")
	}
	if cfg.Forwarder == nil {
		return nil, errors.New("session: sem forwarder")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	interval := cfg.Interval
	if interval <= 0 {
		interval = defaultInterval
	}
	portFree := cfg.PortFree
	if portFree == nil {
		portFree = plan.LocalPortFree
	}
	cfg.PortFree = portFree

	saveDelay := cfg.SaveDelay
	if saveDelay <= 0 {
		saveDelay = 2 * time.Second
	}

	s := &Session{
		cfg:       cfg,
		now:       now,
		interval:  interval,
		saveDelay: saveDelay,
		changed:   make(chan struct{}, 1),
		inbox:     make(chan func(), 64),
		scans:     make(chan scanResult, 1),
		rows:      map[int]*Row{},
		scanReq:   make(chan struct{}, 1),
	}
	s.loadChoices()
	s.publish()
	return s, nil
}

func (s *Session) loadChoices() {
	if s.cfg.Store == nil {
		return
	}
	host, err := s.cfg.Store.Load()
	if err != nil {
		s.setNotice(err.Error())
	}
	for _, c := range host.Choices {
		s.rows[c.RemotePort] = &Row{
			RemotePort: c.RemotePort,
			LocalPort:  c.LocalPort,
			Alias:      c.Alias,
			RemoteHost: c.RemoteHost,
			Pinned:     c.Pinned,
			Wanted:     c.Wanted,
		}
	}
}

// Snapshot is a single atomic load: the UI can never block on ssh by reading it.
func (s *Session) Snapshot() Snapshot { return *s.snap.Load() }

// Changed is a doorbell, not a stream. It carries nothing because the answer to
// a ring is always a fresh snapshot, so a dropped ring costs latency, never
// correctness.
func (s *Session) Changed() <-chan struct{} { return s.changed }

// Toggle flips whether the user wants a port forwarded.
func (s *Session) Toggle(remotePort int) {
	s.do(func() {
		r, ok := s.rows[remotePort]
		if !ok {
			return
		}
		if r.Wanted {
			r.Wanted = false
			s.cfg.Forwarder.Stop(remotePort)
		} else {
			r.Wanted = true
			s.cfg.Forwarder.Start(s.forwardFor(r))
		}
		s.dirty = true
		s.publish()
	})
}

// SetAll turns every visible row on or off at once.
func (s *Session) SetAll(on bool) {
	s.do(func() {
		for _, r := range s.visibleRows() {
			if r.Wanted == on {
				continue
			}
			r.Wanted = on
			if on {
				s.cfg.Forwarder.Start(s.forwardFor(r))
			} else {
				s.cfg.Forwarder.Stop(r.RemotePort)
			}
		}
		s.dirty = true
		s.publish()
	})
}

// SetShowAll controls whether infrastructure ports are listed.
func (s *Session) SetShowAll(v bool) {
	s.do(func() {
		s.cfg.ShowAll = v
		s.publish()
	})
}

// Refresh asks for a scan now.
func (s *Session) Refresh() {
	select {
	case s.scanReq <- struct{}{}:
	default:
	}
}

// SetLocalPort pins a local port for a row.
//
// This is the one synchronous call: its error is what the edit prompt has to
// show, and the only I/O it does is a local listen probe. A running tunnel is
// moved here rather than by the caller, because a caller that sequenced
// stop-then-start could not undo the first step if the second failed.
func (s *Session) SetLocalPort(remotePort, localPort int) error {
	errc := make(chan error, 1)
	s.do(func() { errc <- s.setLocalPort(remotePort, localPort) })
	return <-errc
}

func (s *Session) setLocalPort(remotePort, localPort int) error {
	r, ok := s.rows[remotePort]
	if !ok {
		return fmt.Errorf("a porta %d sumiu do remoto", remotePort)
	}
	if localPort == r.LocalPort {
		return nil
	}
	if localPort < 1024 || localPort > 65535 {
		return fmt.Errorf("porta fora do intervalo (1024–65535)")
	}
	for _, other := range s.rows {
		if other.RemotePort != remotePort && other.LocalPort == localPort {
			return fmt.Errorf("porta local %d já é usada por %s", localPort, other.Alias)
		}
	}
	if !s.cfg.PortFree(localPort) {
		return fmt.Errorf("porta local %d já está em uso nesta máquina", localPort)
	}

	wasWanted := r.Wanted
	if wasWanted {
		s.cfg.Forwarder.Stop(remotePort)
	}
	r.LocalPort = localPort
	r.Pinned = true
	if wasWanted {
		s.cfg.Forwarder.Start(s.forwardFor(r))
	}
	s.dirty = true
	s.publish()
	return nil
}

// Run owns the rows until ctx is done.
func (s *Session) Run(ctx context.Context) error {
	tick := s.cfg.Tick
	if tick == nil {
		t := time.NewTicker(s.interval)
		defer t.Stop()
		tick = t.C
	}

	var saveTimer <-chan time.Time
	events := s.cfg.Forwarder.Events()

	for {
		select {
		case <-ctx.Done():
			s.saveNow()
			return ctx.Err()

		case fn := <-s.inbox:
			fn()
			if s.dirty && saveTimer == nil {
				saveTimer = time.After(s.saveDelay)
			}

		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			s.applyEvent(ctx, ev)
			if s.dirty && saveTimer == nil {
				saveTimer = time.After(s.saveDelay)
			}

		case res := <-s.scans:
			s.applyScan(res)

		case <-tick:
			s.startScan(ctx)

		case <-s.scanReq:
			s.startScan(ctx)

		case <-saveTimer:
			saveTimer = nil
			s.saveNow()
		}
	}
}

func (s *Session) applyEvent(ctx context.Context, ev tunnel.Event) {
	if ev.Kind == tunnel.LinkEvent {
		was := s.link
		s.link = ev.Link
		s.fatal = ev.Fatal
		s.linkErr = ""
		if ev.Err != nil {
			s.linkErr = linkMessage(ev.Err, s.cfg.Host)
		}
		// A fresh connection is the moment to auto-start and to scan: before it,
		// there is nothing to talk to.
		if ev.Link == tunnel.LinkUp && was != tunnel.LinkUp {
			s.autoStart()
			s.startScan(ctx)
		}
		s.publish()
		return
	}

	r, ok := s.rows[ev.Port]
	if !ok {
		return
	}
	r.State = ev.State
	r.Err, r.Detail = "", ""
	if ev.Err != nil {
		r.Err, r.Detail = rowMessage(ev.Err, r.LocalPort), ev.Err.Raw
		// The supervisor gives up on a failure the user has to resolve, so the
		// row must stop claiming it is wanted or it would look like it is still
		// trying.
		if !ev.Err.Kind.Retryable() {
			r.Wanted = false
			s.dirty = true
		}
	}
	s.publish()
}

func (s *Session) autoStart() {
	if !s.cfg.AutoStart {
		return
	}
	for _, r := range s.rows {
		if r.Wanted {
			s.cfg.Forwarder.Start(s.forwardFor(r))
		}
	}
}

// startScan runs one discovery at a time. A tick arriving while a scan is in
// flight is dropped rather than queued: falling behind would pile up ssh calls
// on a host that is already slow.
func (s *Session) startScan(ctx context.Context) {
	if s.scanning || s.cfg.Runner == nil || s.link != tunnel.LinkUp {
		return
	}
	s.scanning = true
	s.publish()

	host, runner, all := s.cfg.Host, s.cfg.Runner, true
	go func() {
		scanCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		ports, err := discover.Discover(scanCtx, runner, host, all)
		select {
		case s.scans <- scanResult{ports: ports, err: err}:
		case <-ctx.Done():
		}
	}()
}

// applyScan folds a discovery into the rows.
//
// The invariant that matters most: a failed scan changes nothing. One flaky ssh
// call must never remove a row, drop a checkbox or tear down a tunnel.
func (s *Session) applyScan(res scanResult) {
	s.scanning = false
	if res.err != nil {
		s.scanErr = fmt.Sprintf("varredura falhou: %v", res.err)
		s.publish()
		return
	}
	s.scanErr = ""
	s.lastScan = s.now()

	names := plan.Aliases(res.ports)
	seen := map[int]bool{}
	taken := s.takenLocalPorts()
	var remaps [][2]int

	for _, p := range res.ports {
		seen[p.Port] = true
		r, ok := s.rows[p.Port]
		if !ok {
			local, remapped := plan.Assign(p.Port, taken, s.cfg.PortFree)
			// Only a row the user can actually see is worth telling them about.
			// Every system port under 1024 is "remapped" by definition, and
			// announcing those buries the one remap that matters.
			if remapped && local != 0 && !p.IsNoise() {
				remaps = append(remaps, [2]int{p.Port, local})
			}
			taken[local] = true
			r = &Row{RemotePort: p.Port, LocalPort: local}
			s.rows[p.Port] = r
		}
		// A rescan may only refresh what the remote knows. It never writes
		// Wanted, Pinned, LocalPort or State.
		r.Alias = names[p.Port]
		r.RemoteHost = remoteHostFor(p)
		r.Noisy = p.IsNoise()
		r.Present = true
	}

	// A port that disappeared keeps its row and its tunnel. A dev server
	// restarting for three seconds must not cost the user their checkbox.
	for port, r := range s.rows {
		if !seen[port] {
			r.Present = false
		}
	}
	// One message for the whole scan: a silent remap is worse than the
	// collision, but six separate lines about it are worse than one.
	switch len(remaps) {
	case 0:
	case 1:
		s.setNotice(fmt.Sprintf("porta %d remapeada para %d (a local estava ocupada)",
			remaps[0][0], remaps[0][1]))
	default:
		s.setNotice(fmt.Sprintf("%d portas remapeadas (as locais estavam ocupadas)", len(remaps)))
	}
	s.publish()
}

// setNotice records a one-off message and bumps its sequence, so a consumer can
// tell a new notice from the same one being carried along by later snapshots.
func (s *Session) setNotice(text string) {
	s.notice = text
	s.noticeSeq++
}

func (s *Session) takenLocalPorts() map[int]bool {
	taken := make(map[int]bool, len(s.rows))
	for _, r := range s.rows {
		if r.LocalPort != 0 {
			taken[r.LocalPort] = true
		}
	}
	return taken
}

func (s *Session) forwardFor(r *Row) sshmux.Forward {
	return sshmux.Forward{LocalPort: r.LocalPort, RemoteHost: r.RemoteHost, RemotePort: r.RemotePort}
}

func (s *Session) visibleRows() []*Row {
	out := make([]*Row, 0, len(s.rows))
	for _, r := range s.rows {
		if s.cfg.ShowAll || !r.Noisy || r.Wanted || r.Pinned {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RemotePort < out[j].RemotePort })
	return out
}

// do queues work onto the Run goroutine. The send blocks if the queue is full,
// because dropping a user's keystroke is a bug; the queue drains fast because
// Run never performs I/O inline.
func (s *Session) do(fn func()) { s.inbox <- fn }

// publish swaps in a new immutable snapshot and rings the doorbell.
func (s *Session) publish() {
	s.rev++
	rows := s.visibleRows()
	out := make([]Row, len(rows))
	for i, r := range rows {
		out[i] = *r
	}
	s.snap.Store(&Snapshot{
		Host:      s.cfg.Host,
		Link:      s.link,
		LinkErr:   s.linkErr,
		Fatal:     s.fatal,
		Rows:      out,
		LastScan:  s.lastScan,
		Scanning:  s.scanning,
		ScanErr:   s.scanErr,
		Notice:    s.notice,
		NoticeSeq: s.noticeSeq,
		Rev:       s.rev,
	})
	select {
	case s.changed <- struct{}{}:
	default: // a ring is already pending; the consumer will read the latest anyway
	}
}

func (s *Session) saveNow() {
	if !s.dirty || s.cfg.Store == nil {
		return
	}
	s.dirty = false
	choices := make([]state.Choice, 0, len(s.rows))
	for _, r := range s.rows {
		choices = append(choices, state.Choice{
			RemotePort: r.RemotePort,
			LocalPort:  r.LocalPort,
			Pinned:     r.Pinned,
			Wanted:     r.Wanted,
			Alias:      r.Alias,
			RemoteHost: r.RemoteHost,
			LastSeen:   s.now(),
		})
	}
	if err := s.cfg.Store.Save(state.Host{Host: s.cfg.Host, Choices: choices}); err != nil {
		s.setNotice(fmt.Sprintf("não consegui gravar %s: %v", s.cfg.Store.Path(), err))
		s.publish()
	}
}

// remoteHostFor picks which address the remote service is actually on.
//
// Hardcoding "localhost" is a real bug source: on the remote it can resolve to
// ::1 where the service only listens on v4.
func remoteHostFor(p discover.Port) string {
	for _, b := range p.Binds {
		if b == "127.0.0.1" || b == "0.0.0.0" {
			return "127.0.0.1"
		}
	}
	if len(p.Binds) > 0 {
		return "::1"
	}
	return "127.0.0.1"
}

// rowMessage turns a classified ssh failure into something the user can act on.
func rowMessage(e *sshmux.Error, localPort int) string {
	switch e.Kind {
	case sshmux.KindBind:
		return fmt.Sprintf("porta local %d já está em uso", localPort)
	case sshmux.KindRefused:
		return "o serviço recusou a conexão no remoto"
	case sshmux.KindTimeout:
		return "tempo esgotado ao falar com o ssh"
	case sshmux.KindNoMaster:
		return "conexão caiu"
	default:
		return e.Raw
	}
}

func linkMessage(e *sshmux.Error, host string) string {
	switch e.Kind {
	case sshmux.KindAuth:
		return fmt.Sprintf("autenticação falhou — rode `ssh %s` uma vez para resolver", host)
	case sshmux.KindHostKey:
		return fmt.Sprintf("host key não confere — rode `ssh %s` uma vez para resolver", host)
	case sshmux.KindRefused:
		return fmt.Sprintf("não consegui alcançar %s", host)
	default:
		return e.Raw
	}
}
