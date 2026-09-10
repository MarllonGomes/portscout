package sshmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The spec string must be byte-identical between forward and cancel: ssh matches
// a cancel against the listen/connect pair it was given, so a spec built twice
// from different fields would leave the listener behind forever.
func TestForwardSpecIsStable(t *testing.T) {
	f := Forward{LocalPort: 3102, RemoteHost: "127.0.0.1", RemotePort: 3100}
	want := "127.0.0.1:3102:127.0.0.1:3100"
	if got := f.Spec(); got != want {
		t.Errorf("Spec() = %q, want %q", got, want)
	}
	if f.Spec() != f.Spec() {
		t.Error("Spec() is not deterministic")
	}
}

func TestForwardSpecDefaultsTheRemoteSide(t *testing.T) {
	f := Forward{LocalPort: 3102, RemotePort: 3100}
	if got, want := f.Spec(), "127.0.0.1:3102:127.0.0.1:3100"; got != want {
		t.Errorf("Spec() = %q, want %q", got, want)
	}
}

// ssh(1): "IPv6 addresses can be specified by enclosing the address in square
// brackets." Without them the spec is ambiguous and ssh rejects it.
func TestForwardSpecBracketsAnIPv6Remote(t *testing.T) {
	f := Forward{LocalPort: 3102, RemoteHost: "::1", RemotePort: 3100}
	if got, want := f.Spec(), "127.0.0.1:3102:[::1]:3100"; got != want {
		t.Errorf("Spec() = %q, want %q", got, want)
	}
}

func TestMasterArgs(t *testing.T) {
	got := masterArgs("dev", "/run/p/x.sock")
	want := []string{
		"-M", "-N", "-S", "/run/p/x.sock",
		"-o", "ControlPersist=no",
		"-o", "BatchMode=yes",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"dev",
	}
	assertArgs(t, got, want)
}

// ControlPersist=no is what makes "the tunnels die with the dashboard" true by
// construction rather than by a deferred cleanup we might not reach.
func TestMasterArgsNeverPersists(t *testing.T) {
	joined := strings.Join(masterArgs("dev", "/tmp/x.sock"), " ")
	if !strings.Contains(joined, "ControlPersist=no") {
		t.Errorf("master must not outlive us: %s", joined)
	}
}

func TestControlArgs(t *testing.T) {
	f := Forward{LocalPort: 3102, RemotePort: 3100}
	cases := []struct {
		name string
		op   string
		fwd  *Forward
		want []string
	}{
		{
			name: "forward",
			op:   "forward",
			fwd:  &f,
			want: []string{"-S", "/s", "-O", "forward", "-L", "127.0.0.1:3102:127.0.0.1:3100", "dev"},
		},
		{
			name: "cancel",
			op:   "cancel",
			fwd:  &f,
			want: []string{"-S", "/s", "-O", "cancel", "-L", "127.0.0.1:3102:127.0.0.1:3100", "dev"},
		},
		{
			name: "check takes no forward",
			op:   "check",
			fwd:  nil,
			want: []string{"-S", "/s", "-O", "check", "dev"},
		},
		{
			name: "exit takes no forward",
			op:   "exit",
			fwd:  nil,
			want: []string{"-S", "/s", "-O", "exit", "dev"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assertArgs(t, controlArgs("dev", "/s", c.op, c.fwd), c.want)
		})
	}
}

// The scan rides the master, but must not become one: without ControlMaster=no a
// second -M could be negotiated and we would own two connections.
func TestRunnerArgs(t *testing.T) {
	got := runnerArgs("dev", "/s", "echo hi")
	want := []string{"-S", "/s", "-o", "ControlMaster=no", "-o", "BatchMode=yes", "dev", "echo hi"}
	assertArgs(t, got, want)
}

// BatchMode everywhere: a child that decides to prompt for a passphrase would
// fight the dashboard for the terminal and leave the screen unrecoverable.
func TestEveryInvocationIsBatchMode(t *testing.T) {
	for name, args := range map[string][]string{
		"master": masterArgs("dev", "/s"),
		"runner": runnerArgs("dev", "/s", "true"),
	} {
		if !strings.Contains(strings.Join(args, " "), "BatchMode=yes") {
			t.Errorf("%s may prompt: %v", name, args)
		}
	}
}

func TestSocketPathIsStablePerHost(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")

	a, err := SocketPath("dev")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := SocketPath("dev")
	if a != b {
		t.Errorf("path must be deterministic: %q vs %q", a, b)
	}
	other, _ := SocketPath("prod")
	if a == other {
		t.Error("different hosts must not share a control socket")
	}
	if filepath.Ext(a) != ".sock" {
		t.Errorf("path = %q, want a .sock suffix", a)
	}
}

// A host name goes into the path as a hash, never verbatim: "user@10.0.0.1" and
// IPv6 literals are not filesystem-safe, and the path budget is tiny.
func TestSocketPathHandlesAwkwardHostNames(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1000")
	for _, host := range []string{"user@10.0.0.1", "[::1]:2222", "a/../../etc/passwd"} {
		got, err := SocketPath(host)
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		if strings.Contains(got, "@") || strings.Contains(got, "..") || strings.Contains(got, ":") {
			t.Errorf("%s produced an unsafe path: %q", host, got)
		}
	}
}

// sun_path is 108 bytes on Linux and 104 on macOS. Overflowing it fails at bind
// time with a confusing error, so it is caught up front instead.
func TestSocketPathRefusesToOverflowSunPath(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "/"+strings.Repeat("d", 200))
	if _, err := SocketPath("dev"); err == nil {
		t.Error("expected an error for a path longer than sun_path")
	}
}

func TestSocketPathFallsBackWithoutXDG(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	got, err := SocketPath("dev")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, os.TempDir()) {
		t.Errorf("path = %q, want a fallback under %q", got, os.TempDir())
	}
}

func assertArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d args %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("arg %d = %q, want %q\nfull: %v", i, got[i], want[i], got)
		}
	}
}
