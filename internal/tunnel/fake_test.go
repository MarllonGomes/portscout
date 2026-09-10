package tunnel

import (
	"context"
	"sync"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/sshmux"
)

// fakeClient stands in for ssh(1). Every call is recorded and every outcome is
// scripted, so the supervisor's state machine is exercised without a process,
// a socket or a second of real time.
type fakeClient struct {
	mu sync.Mutex

	// scripted outcomes
	startErr    error
	forwardErrs map[int]error // by local port
	cancelErrs  map[int]error

	// observed calls
	starts    int
	forwards  []int
	cancels   []int
	stops     int
	exitedCh  chan error
	forwardCh chan int // when non-nil, receives every forward as it happens
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		forwardErrs: map[int]error{},
		cancelErrs:  map[int]error{},
		exitedCh:    make(chan error, 1),
	}
}

func (f *fakeClient) Start(context.Context) (<-chan error, error) {
	f.mu.Lock()
	f.starts++
	err := f.startErr
	// A reconnect needs a fresh exit channel: the old one is already spent.
	f.exitedCh = make(chan error, 1)
	ch := f.exitedCh
	f.mu.Unlock()

	if err != nil {
		return nil, err
	}
	return ch, nil
}

func (f *fakeClient) Forward(_ context.Context, fwd sshmux.Forward) error {
	f.mu.Lock()
	f.forwards = append(f.forwards, fwd.LocalPort)
	err := f.forwardErrs[fwd.LocalPort]
	ch := f.forwardCh
	f.mu.Unlock()
	if ch != nil {
		ch <- fwd.LocalPort
	}
	return err
}

func (f *fakeClient) Cancel(_ context.Context, fwd sshmux.Forward) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, fwd.LocalPort)
	return f.cancelErrs[fwd.LocalPort]
}

func (f *fakeClient) Check(context.Context) error { return nil }

func (f *fakeClient) Stop(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops++
	return nil
}

func (f *fakeClient) Runner() discover.Runner { return nil }

// killMaster makes the current connection die, the way a dropped link would.
func (f *fakeClient) killMaster(err error) {
	f.mu.Lock()
	ch := f.exitedCh
	f.mu.Unlock()
	ch <- err
	close(ch)
}

func (f *fakeClient) setForwardErr(localPort int, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forwardErrs[localPort] = err
}

func (f *fakeClient) counts() (starts, stops int, forwards, cancels []int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts, f.stops, append([]int(nil), f.forwards...), append([]int(nil), f.cancels...)
}

// fakeClock hands out timer channels the test fires by hand, so backoff is
// asserted as a sequence of durations instead of by sleeping through it.
type fakeClock struct {
	mu       sync.Mutex
	asked    []time.Duration
	pending  []chan time.Time
	notifyCh chan time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{notifyCh: make(chan time.Duration, 16)}
}

func (c *fakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	ch := make(chan time.Time, 1)
	c.asked = append(c.asked, d)
	c.pending = append(c.pending, ch)
	c.mu.Unlock()
	c.notifyCh <- d
	return ch
}

// fire releases the oldest outstanding timer.
func (c *fakeClock) fire() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.pending) == 0 {
		return
	}
	ch := c.pending[0]
	c.pending = c.pending[1:]
	ch <- time.Now()
}

func (c *fakeClock) durations() []time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]time.Duration(nil), c.asked...)
}

// waitTimer blocks until a timer is requested, so a test never races the loop.
func (c *fakeClock) waitTimer(timeout time.Duration) (time.Duration, bool) {
	select {
	case d := <-c.notifyCh:
		return d, true
	case <-time.After(timeout):
		return 0, false
	}
}
