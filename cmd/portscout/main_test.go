package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct{ out string }

func (f fakeRunner) Run(context.Context, string, string) ([]byte, error) {
	return []byte(f.out), nil
}

const fakeRemote = `portscout-section:ss
LISTEN 0 511 127.0.0.1:3100 0.0.0.0:* users:(("next-server",pid=1,fd=22))
LISTEN 0 4096 127.0.0.1:3102 0.0.0.0:* users:(("rootlesskit",pid=2,fd=24))
portscout-section:docker
{"ID":"a1","Names":"postgres-1","Ports":"127.0.0.1:3102->5432/tcp"}
`

func TestRunListPrintsPortsAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"list", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "3100") || !strings.Contains(out.String(), "postgres-1") {
		t.Errorf("expected ports in output:\n%s", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("list must not create the config file")
	}
}

func TestRunScanWritesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"scan", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	for _, want := range []string{"remote_port: 3100", "remote_port: 3102", "tag: auto", "postgres-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRunScanReportsRemap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	run([]string{"scan", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(p int) bool { return p != 3100 },
	})

	if !strings.Contains(out.String(), "remapeada") {
		t.Errorf("a remap must be announced:\n%s", out.String())
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
	if code := run([]string{"scan"}, options{stdout: &out, stderr: &out}); code == 0 {
		t.Fatal("missing host must exit non-zero")
	}
}

// Go's flag package stops parsing at the first positional argument, so
// "scan dev --config=X" silently ignored every flag after the host. Putting the
// host last is the natural way to type this, so flags must be honoured wherever
// they appear.
func TestRunAcceptsFlagsAfterTheHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"scan", "dev", "--config=" + path}, options{
		runner:   fakeRunner{out: fakeRemote},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("--config after the host was ignored: %v\n%s", err, out.String())
	}
}

func TestRunAcceptsSpaceSeparatedFlagAfterHost(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"scan", "dev", "--config", path}, options{
		runner:   fakeRunner{out: fakeRemote},
		stdout:   &out,
		stderr:   &out,
		portFree: func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("space-separated --config after the host was ignored: %v", err)
	}
}

// The remap notice used to be printed from the planned assignments, before the
// merge decided anything. On a rescan the planner reassigns ports that the
// merge then ignores, so the user was told about remaps that never happened.
func TestRunDoesNotAnnounceRemapsOnAnUnchangedRescan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	opts := func(out *bytes.Buffer) options {
		return options{
			runner:     fakeRunner{out: fakeRemote},
			configPath: path,
			stdout:     out,
			stderr:     out,
			portFree:   func(int) bool { return true },
		}
	}

	var first bytes.Buffer
	if code := run([]string{"scan", "dev"}, opts(&first)); code != 0 {
		t.Fatalf("first scan failed: %s", first.String())
	}

	var second bytes.Buffer
	if code := run([]string{"scan", "dev"}, opts(&second)); code != 0 {
		t.Fatalf("second scan failed: %s", second.String())
	}
	if strings.Contains(second.String(), "remapeada") {
		t.Errorf("rescan must not announce remaps:\n%s", second.String())
	}
}

func TestRunKeepsTwoSpaceIndent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer
	run([]string{"scan", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(int) bool { return true },
	})

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "\n  - remote_host:") {
		t.Errorf("expected a two-space list indent, got:\n%s", written)
	}
}
