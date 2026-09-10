package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
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

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"frobnicate"}, options{stdout: &out, stderr: &out}); code == 0 {
		t.Fatal("unknown command must exit non-zero")
	}
}

func TestRunRequiresHost(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"list"}, options{stdout: &out, stderr: &out}); code == 0 {
		t.Fatal("missing host must exit non-zero")
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
