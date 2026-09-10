package sshmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// defaultTimeout bounds every control command. A half-dead link must surface as
// one failed row, never as a frozen supervisor.
const defaultTimeout = 5 * time.Second

// Client is the slice of ssh(1) the supervisor needs. Tests replace it whole, so
// nothing below this interface ever runs in a test.
type Client interface {
	// Start launches the master and returns once it is authenticated and
	// answering on the control socket. The returned channel yields exactly one
	// value, when the master exits, and is then closed.
	Start(ctx context.Context) (exited <-chan error, err error)
	Forward(ctx context.Context, f Forward) error
	Cancel(ctx context.Context, f Forward) error
	Check(ctx context.Context) error
	Stop(ctx context.Context) error
	// Runner scans the remote over the same connection, so a rescan costs no
	// handshake.
	Runner() discover.Runner
}

// OpenSSH drives the real ssh binary.
type OpenSSH struct {
	Host    string
	Socket  string
	Timeout time.Duration

	bin string

	mu     sync.Mutex
	master *exec.Cmd
	stderr *ringBuffer
}

var _ Client = (*OpenSSH)(nil)

// New resolves everything that can fail before the dashboard takes over the
// screen, so a misconfigured machine produces a plain error message instead of a
// broken TUI.
func New(host string) (*OpenSSH, error) {
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return nil, fmt.Errorf("ssh não encontrado no PATH: %w", err)
	}
	socket, err := SocketPath(host)
	if err != nil {
		return nil, err
	}
	return &OpenSSH{Host: host, Socket: socket, Timeout: defaultTimeout, bin: bin}, nil
}

func (c *OpenSSH) timeout() time.Duration {
	if c.Timeout > 0 {
		return c.Timeout
	}
	return defaultTimeout
}

func (c *OpenSSH) Start(ctx context.Context) (<-chan error, error) {
	if err := os.MkdirAll(filepath.Dir(c.Socket), 0o700); err != nil {
		return nil, fmt.Errorf("criando diretório do socket: %w", err)
	}
	if err := c.clearStaleMaster(ctx); err != nil {
		return nil, err
	}

	ring := &ringBuffer{max: 4096}
	cmd := exec.Command(c.bin, masterArgs(c.Host, c.Socket)...)
	cmd.Stdin = nil // never let a child prompt: it would fight the TUI for the tty
	cmd.Stdout = nil
	cmd.Stderr = ring
	// Its own process group: ssh can never read our terminal, and we get a group
	// to kill on the way out.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		return nil, Classify("master", "", err)
	}

	c.mu.Lock()
	c.master, c.stderr = cmd, ring
	c.mu.Unlock()

	exited := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		if err != nil {
			err = Classify("master", ring.String(), err)
		}
		exited <- err
		close(exited)
	}()

	if err := c.waitReady(ctx, exited, ring); err != nil {
		_ = c.Stop(context.WithoutCancel(ctx))
		return nil, err
	}
	return exited, nil
}

// waitReady polls -O check rather than sleeping: the master answering on its
// socket is the only honest definition of "connected", and auth over a slow link
// can take seconds.
func (c *OpenSSH) waitReady(ctx context.Context, exited <-chan error, ring *ringBuffer) error {
	deadline := time.Now().Add(30 * time.Second)
	for {
		select {
		case err := <-exited:
			// The master gave up before it was ready; its stderr says why.
			if err != nil {
				return err
			}
			return Classify("master", ring.String(), errors.New("master saiu antes de conectar"))
		case <-ctx.Done():
			return Classify("master", ring.String(), ctx.Err())
		default:
		}

		if err := c.Check(ctx); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return Classify("master", ring.String(), context.DeadlineExceeded)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// clearStaleMaster deals with the socket a killed run left behind.
//
// A live master from a previous process cannot be adopted: there is no mux
// command that enumerates its existing forwards, so our first -O forward would
// come back as a bind error we would have to lie about. Kill it and start clean.
func (c *OpenSSH) clearStaleMaster(ctx context.Context) error {
	if err := c.Check(ctx); err == nil {
		_ = c.control(ctx, "exit", nil)
	}
	// ssh refuses to -M onto a path that already exists, so a dead socket file
	// has to go even when nothing answered on it.
	if err := os.Remove(c.Socket); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removendo socket órfão %s: %w", c.Socket, err)
	}
	return nil
}

func (c *OpenSSH) Forward(ctx context.Context, f Forward) error { return c.control(ctx, "forward", &f) }
func (c *OpenSSH) Cancel(ctx context.Context, f Forward) error  { return c.control(ctx, "cancel", &f) }
func (c *OpenSSH) Check(ctx context.Context) error              { return c.control(ctx, "check", nil) }

func (c *OpenSSH) control(ctx context.Context, op string, f *Forward) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout())
	defer cancel()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, c.bin, controlArgs(c.Host, c.Socket, op, f)...)
	cmd.Stdin = nil
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Classify(op, stderr.String(), err)
	}
	return nil
}

// Stop takes the master down in three escalating steps. ControlPersist=no means
// even the bluntest of them takes every forward with it.
func (c *OpenSSH) Stop(ctx context.Context) error {
	c.mu.Lock()
	cmd := c.master
	c.master = nil
	c.mu.Unlock()

	exitCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	_ = c.control(exitCtx, "exit", nil)

	if cmd == nil || cmd.Process == nil {
		return nil
	}
	done := make(chan struct{})
	go func() {
		_, _ = cmd.Process.Wait()
		close(done)
	}()

	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	_ = os.Remove(c.Socket)
	return nil
}

func (c *OpenSSH) Runner() discover.Runner { return muxRunner{c} }

// muxRunner runs the discovery script over the established master.
//
// ssh silently falls back to a fresh connection when the control socket is gone,
// and there is no option to forbid that, so the caller is responsible for only
// scanning while the link is known to be up.
type muxRunner struct{ c *OpenSSH }

func (m muxRunner) Run(ctx context.Context, host, script string) ([]byte, error) {
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, m.c.bin, runnerArgs(host, m.c.Socket, script)...)
	cmd.Stdin = nil
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, Classify("scan", stderr.String(), err)
	}
	return out, nil
}

// ringBuffer keeps the tail of a long-lived stderr. The master can run for hours,
// and only its last words matter when it finally dies.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (r *ringBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.buf = append(r.buf, p...)
	if len(r.buf) > r.max {
		r.buf = r.buf[len(r.buf)-r.max:]
	}
	return len(p), nil
}

func (r *ringBuffer) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return string(r.buf)
}
