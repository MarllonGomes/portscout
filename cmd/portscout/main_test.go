package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

type fakeRunner struct{ out string }

func (f fakeRunner) Run(context.Context, string, string) ([]byte, error) {
	return []byte(f.out), nil
}

// The sshd line is what makes --all observable: it is filtered by default and
// shown with the flag, so the flag-placement tests below can assert on it.
const fakeRemote = `portscout-section:ss
LISTEN 0 128 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
LISTEN 0 511 127.0.0.1:3100 0.0.0.0:* users:(("next-server",pid=2,fd=22))
LISTEN 0 4096 127.0.0.1:3102 0.0.0.0:* users:(("rootlesskit",pid=3,fd=24))
portscout-section:docker
{"ID":"a1","Names":"postgres-1","Ports":"127.0.0.1:3102->5432/tcp"}
`

func TestRunListPrintsPorts(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"list", "dev"}, options{
		runner:   fakeRunner{out: fakeRemote},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "3100") || !strings.Contains(out.String(), "postgres-1") {
		t.Errorf("expected ports in output:\n%s", out.String())
	}
	if strings.Contains(out.String(), "sshd") {
		t.Errorf("sshd is noise and must be filtered by default:\n%s", out.String())
	}
}

func TestRunRequiresHost(t *testing.T) {
	for _, args := range [][]string{{}, {"list"}, {"up"}, {"--all"}} {
		var out bytes.Buffer
		var got dashConfig
		code := run(args, options{stdout: &out, stderr: &out, runDash: captureDash(&got)})
		if code == 0 {
			t.Errorf("%v: missing host must exit non-zero", args)
		}
	}
}

// Go's flag package stops parsing at the first positional argument, so
// "list dev --all" silently ignored every flag after the host. Putting the host
// last is the natural way to type this, so flags must be honoured wherever they
// appear.
func TestRunAcceptsFlagsAfterTheHost(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"list", "dev", "--all"}, options{
		runner:   fakeRunner{out: fakeRemote},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "sshd") {
		t.Errorf("--all after the host was ignored:\n%s", out.String())
	}
}

func TestRunAcceptsFlagsBeforeTheHost(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"list", "--all", "dev"}, options{
		runner:   fakeRunner{out: fakeRemote},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "sshd") {
		t.Errorf("--all before the host was ignored:\n%s", out.String())
	}
}

func TestRunReportsNoPorts(t *testing.T) {
	var out bytes.Buffer

	code := run([]string{"list", "dev"}, options{
		runner:   fakeRunner{out: "portscout-section:ss\nportscout-section:docker\n"},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "nenhuma porta") {
		t.Errorf("expected a friendly empty message:\n%s", out.String())
	}
}

// captureDash replaces the whole dashboard so dispatch and flag parsing can be
// asserted without opening ssh or a terminal.
func captureDash(got *dashConfig) func(dashConfig) int {
	return func(cfg dashConfig) int {
		*got = cfg
		return 0
	}
}

// `portscout dev` has to work: the dashboard is the point, so the host alone is
// the whole command line.
func TestRunTreatsAnUnknownFirstWordAsTheHost(t *testing.T) {
	var got dashConfig
	var out bytes.Buffer

	if code := run([]string{"dev"}, options{stdout: &out, stderr: &out, runDash: captureDash(&got)}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if got.host != "dev" {
		t.Errorf("host = %q, want dev", got.host)
	}
	if !got.autoStart {
		t.Error("remembered tunnels should come up by default")
	}
}

func TestRunAcceptsFlagsAroundTheHostWithoutACommand(t *testing.T) {
	for _, args := range [][]string{
		{"dev", "--all"},
		{"--all", "dev"},
		{"dev", "--every=3s", "--no-autostart"},
	} {
		var got dashConfig
		var out bytes.Buffer
		if code := run(args, options{stdout: &out, stderr: &out, runDash: captureDash(&got)}); code != 0 {
			t.Fatalf("%v: exit %d: %s", args, code, out.String())
		}
		if got.host != "dev" {
			t.Errorf("%v: host = %q", args, got.host)
		}
	}
}

func TestRunPassesFlagsThroughToTheDashboard(t *testing.T) {
	var got dashConfig
	var out bytes.Buffer
	code := run([]string{"dev", "--all", "--every", "3s", "--no-autostart", "--state=/tmp/x.json"},
		options{stdout: &out, stderr: &out, runDash: captureDash(&got)})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !got.showAll || got.interval != 3*time.Second || got.autoStart || got.statePath != "/tmp/x.json" {
		t.Errorf("cfg = %+v", got)
	}
}

// A host that happens to be called "list" is still reachable.
func TestHostNamedLikeACommandNeedsTheExplicitVerb(t *testing.T) {
	var got dashConfig
	var out bytes.Buffer
	if code := run([]string{"up", "list"}, options{stdout: &out, stderr: &out, runDash: captureDash(&got)}); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if got.host != "list" {
		t.Errorf("host = %q, want list", got.host)
	}
}

func TestHelpExitsZero(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"--help"}, options{stdout: &out, stderr: &out}); code != 0 {
		t.Errorf("--help exited %d; scripts notice that", code)
	}
	if !strings.Contains(out.String(), "portscout") {
		t.Errorf("expected usage:\n%s", out.String())
	}
}
