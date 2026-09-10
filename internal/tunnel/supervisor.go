// Package tunnel keeps the forwards the user asked for in sync with the
// forwards that actually exist on the SSH connection.
package tunnel

import (
	"context"
	"errors"
	"time"

	"github.com/MarllonGomes/portscout/internal/sshmux"
)

// State is what is happening to one forward right now. It is deliberately
// separate from whether the user wants it: a row can be wanted and failing.
type State int

const (
	Off State = iota
	Connecting
	Up
	Failed
)

func (s State) String() string {
	switch s {
	case Connecting:
		return "connecting"
	case Up:
		return "up"
	case Failed:
		return "failed"
	default:
		return "off"
	}
}

// Link is the health of the shared connection, reported once rather than
// repeated identically on every row.
type Link int

const (
	LinkDown Link = iota
	LinkConnecting
	LinkUp
)

// EventKind separates the two things the supervisor talks about.
type EventKind int

const (
	PortEvent EventKind = iota
	LinkEvent
)

// Event is what the supervisor tells its consumer.
type Event struct {
	Kind  EventKind
	Port  int // remote port, for PortEvent
	State State
	Link  Link
	Err   *sshmux.Error
	Fatal bool // retrying cannot help; the user has to intervene
}

// defaultBackoff is the reconnect schedule; the last entry repeats forever.
var defaultBackoff = []time.Duration{
	time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
	15 * time.Second, 30 * time.Second,
}

// Config wires the supervisor. Client and After are the seams tests replace.
type Config struct {
	Client  sshmux.Client
	Backoff []time.Duration
	After   func(time.Duration) <-chan time.Time
	Buffer  int
}

type entry struct {
	fwd     sshmux.Forward
	wanted  bool
	state   State
	gen     uint64 // bumped per command; results from older generations are stale
	lastErr *sshmux.Error
}

type command struct {
	start bool
	port  int
	fwd   sshmux.Forward
}

type result struct {
	port   int
	gen    uint64
	cancel bool
	err    error
}

// Supervisor owns every forward and the connection they ride on.
//
// One goroutine owns all the state. Commands come in on a channel with a
// blocking send, because dropping a keystroke is a bug; events go out with a
// non-blocking send, because a consumer that is mid-render must never be able to
// stall the tunnels. That asymmetry is only affordable because the loop itself
// never performs I/O: every ssh call runs in a short-lived goroutine and reports
// back through results.
type Supervisor struct {
	client  sshmux.Client
	backoff []time.Duration
	after   func(time.Duration) <-chan time.Time

	commands chan command
	results  chan result
	events   chan Event

	// owned by Run
	entries    map[int]*entry
	link       Link
	masterExit chan error
	tries      int
}

func NewSupervisor(cfg Config) *Supervisor {
	backoff := cfg.Backoff
	if len(backoff) == 0 {
		backoff = defaultBackoff
	}
	after := cfg.After
	if after == nil {
		after = time.After
	}
	buffer := cfg.Buffer
	if buffer <= 0 {
		buffer = 64
	}
	return &Supervisor{
		client:     cfg.Client,
		backoff:    backoff,
		after:      after,
		commands:   make(chan command, 64),
		results:    make(chan result, 64),
		events:     make(chan Event, buffer),
		entries:    map[int]*entry{},
		masterExit: make(chan error, 1),
	}
}

// Events is the stream of state changes. It is closed when Run returns.
func (s *Supervisor) Events() <-chan Event { return s.events }

// Start asks for a forward. It is safe from any goroutine and returns once the
// command is queued, never once the tunnel is up.
func (s *Supervisor) Start(f sshmux.Forward) {
	s.commands <- command{start: true, port: f.RemotePort, fwd: f}
}

// Stop asks for a forward to go away, identified by its remote port.
func (s *Supervisor) Stop(remotePort int) {
	s.commands <- command{port: remotePort}
}

// Run owns everything until ctx is done.
func (s *Supervisor) Run(ctx context.Context) error {
	defer close(s.events)

	var retry <-chan time.Time
	s.dial(ctx)

	for {
		select {
		case <-ctx.Done():
			stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			_ = s.client.Stop(stopCtx)
			cancel()
			return ctx.Err()

		case cmd := <-s.commands:
			s.handleCommand(ctx, cmd)

		case res := <-s.results:
			s.handleResult(res)

		case err := <-s.masterExit:
			if d, ok := s.handleMasterExit(err); ok {
				retry = s.after(d)
			}

		case <-retry:
			retry = nil
			s.dial(ctx)
		}
	}
}

// dial brings the master up in the background. Connecting can take seconds, and
// the loop has to keep accepting toggles while it happens.
func (s *Supervisor) dial(ctx context.Context) {
	s.setLink(LinkConnecting, nil, false)
	go func() {
		exited, err := s.client.Start(ctx)
		if err != nil {
			s.masterExit <- err
			return
		}
		// Republish the connection's death on a channel that outlives it, so the
		// loop can select on one stable channel across reconnects.
		s.masterExit <- nil
		go func() { s.masterExit <- <-exited }()
	}()
}

func (s *Supervisor) handleMasterExit(err error) (time.Duration, bool) {
	if err == nil && s.link != LinkUp {
		// The dial succeeded: the link is up and everything wanted gets replayed.
		s.tries = 0
		s.setLink(LinkUp, nil, false)
		for port, e := range s.entries {
			if e.wanted {
				s.beginForward(port, e)
			}
		}
		return 0, false
	}

	sshErr := asSSHError(err)
	fatal := sshErr != nil && !sshErr.Kind.Retryable()
	s.setLink(LinkDown, sshErr, fatal)

	// A dropped link is not a row failure: the user's choice is intact and the
	// cure is in progress, so wanted rows go back to connecting rather than to
	// an error the user might try to "fix".
	for port, e := range s.entries {
		if e.wanted && e.state != Connecting {
			e.state = Connecting
			s.emit(Event{Kind: PortEvent, Port: port, State: Connecting})
		}
	}

	if fatal {
		return 0, false
	}
	d := s.backoff[min(s.tries, len(s.backoff)-1)]
	s.tries++
	return d, true
}

func (s *Supervisor) handleCommand(ctx context.Context, cmd command) {
	e, ok := s.entries[cmd.port]
	if !ok {
		e = &entry{}
		s.entries[cmd.port] = e
	}
	e.gen++

	if cmd.start {
		e.fwd = cmd.fwd
		e.wanted = true
		if s.link != LinkUp {
			// Remember it; the replay on reconnect will pick it up.
			s.setState(cmd.port, e, Connecting, nil)
			return
		}
		s.beginForward(cmd.port, e)
		return
	}

	e.wanted = false
	wasUp := e.state == Up
	s.setState(cmd.port, e, Off, nil)
	if wasUp && s.link == LinkUp {
		s.beginCancel(cmd.port, e)
	}
}

func (s *Supervisor) beginForward(port int, e *entry) {
	e.state = Connecting
	s.emit(Event{Kind: PortEvent, Port: port, State: Connecting})
	gen, fwd := e.gen, e.fwd
	go func() {
		err := s.client.Forward(context.Background(), fwd)
		s.results <- result{port: port, gen: gen, err: err}
	}()
}

func (s *Supervisor) beginCancel(port int, e *entry) {
	gen, fwd := e.gen, e.fwd
	go func() {
		err := s.client.Cancel(context.Background(), fwd)
		s.results <- result{port: port, gen: gen, cancel: true, err: err}
	}()
}

func (s *Supervisor) handleResult(res result) {
	e, ok := s.entries[res.port]
	// The user changed their mind while this was in flight. Acting on it now
	// would resurrect a row they already turned off.
	if !ok || res.gen != e.gen {
		return
	}
	if res.cancel {
		return
	}
	if res.err == nil {
		s.setState(res.port, e, Up, nil)
		return
	}

	sshErr := asSSHError(res.err)
	// A bind failure is the user's to resolve, so the row stops asking for it;
	// anything transient stays wanted and rides the next reconnect.
	if sshErr != nil && !sshErr.Kind.Retryable() {
		e.wanted = false
	}
	s.setState(res.port, e, Failed, sshErr)
}

func (s *Supervisor) setState(port int, e *entry, st State, err *sshmux.Error) {
	e.state = st
	e.lastErr = err
	s.emit(Event{Kind: PortEvent, Port: port, State: st, Err: err})
}

func (s *Supervisor) setLink(l Link, err *sshmux.Error, fatal bool) {
	s.link = l
	s.emit(Event{Kind: LinkEvent, Link: l, Err: err, Fatal: fatal})
}

// emit never blocks. Losing an event is safe because it is only a hint: the
// consumer's authority is the snapshot it reads afterwards, and any state that
// matters is sticky.
func (s *Supervisor) emit(ev Event) {
	select {
	case s.events <- ev:
	default:
	}
}

func asSSHError(err error) *sshmux.Error {
	if err == nil {
		return nil
	}
	var e *sshmux.Error
	if errors.As(err, &e) {
		return e
	}
	return sshmux.Classify("master", err.Error(), err)
}
