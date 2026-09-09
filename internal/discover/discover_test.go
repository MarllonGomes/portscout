package discover

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

type fakeRunner struct {
	out []byte
	err error
}

func (f fakeRunner) Run(context.Context, string, string) ([]byte, error) { return f.out, f.err }

// combined builds the exact stdout shape the remote script produces.
func combined(t *testing.T, withDocker bool) []byte {
	t.Helper()
	ss, err := os.ReadFile("testdata/ss.txt")
	if err != nil {
		t.Fatal(err)
	}
	out := []byte(markerSS + "\n")
	out = append(out, ss...)
	out = append(out, []byte("\n"+markerDocker+"\n")...)
	if withDocker {
		dk, err := os.ReadFile("testdata/docker.json")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, dk...)
	}
	return out
}

func TestDiscoverLabelsContainersAndFiltersNoise(t *testing.T) {
	ports, err := Discover(context.Background(), fakeRunner{out: combined(t, true)}, "dev", false)
	if err != nil {
		t.Fatal(err)
	}

	byPort := map[int]Port{}
	for _, p := range ports {
		byPort[p.Port] = p
	}

	if _, ok := byPort[22]; ok {
		t.Error("port 22 (sshd) should be filtered as noise")
	}
	if _, ok := byPort[53]; ok {
		t.Error("port 53 (systemd-resolve) should be filtered as noise")
	}

	pg, ok := byPort[3102]
	if !ok {
		t.Fatal("port 3102 missing")
	}
	if pg.Container != "couple-community-postgres-1" {
		t.Errorf("3102 container: got %q", pg.Container)
	}
	if pg.Label() != "couple-community-postgres-1" {
		t.Errorf("3102 label: got %q", pg.Label())
	}

	next, ok := byPort[3100]
	if !ok {
		t.Fatal("port 3100 missing")
	}
	if next.Container != "" {
		t.Errorf("3100 should not be a container, got %q", next.Container)
	}
	if next.Label() != "next-server (v1" {
		t.Errorf("3100 label: got %q", next.Label())
	}
}

func TestDiscoverCollapsesDualStack(t *testing.T) {
	out := []byte(markerSS + `
LISTEN 0 128 0.0.0.0:9000 0.0.0.0:* users:(("app",pid=1,fd=3))
LISTEN 0 128 [::]:9000 [::]:* users:(("app",pid=1,fd=4))
` + markerDocker + "\n")

	ports, err := Discover(context.Background(), fakeRunner{out: out}, "dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("expected 1 collapsed port, got %d: %+v", len(ports), ports)
	}
	if len(ports[0].Binds) != 2 {
		t.Errorf("expected 2 binds recorded, got %+v", ports[0].Binds)
	}
}

func TestDiscoverIncludeAllKeepsNoise(t *testing.T) {
	ports, err := Discover(context.Background(), fakeRunner{out: combined(t, true)}, "dev", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ports {
		if p.Port == 22 {
			return
		}
	}
	t.Error("with includeAll, port 22 should be present")
}

func TestDiscoverWithoutDockerStillReturnsHostPorts(t *testing.T) {
	ports, err := Discover(context.Background(), fakeRunner{out: combined(t, false)}, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) == 0 {
		t.Fatal("expected host ports even without docker")
	}
	for _, p := range ports {
		if p.Container != "" {
			t.Errorf("no docker output, but port %d got container %q", p.Port, p.Container)
		}
	}
}

func TestDiscoverPropagatesRunnerError(t *testing.T) {
	_, err := Discover(context.Background(), fakeRunner{err: errors.New("ssh: connect failed")}, "dev", false)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "ssh: connect failed") {
		t.Errorf("error should wrap the runner failure, got %v", err)
	}
}

func TestRemoteScriptHasRootlessFallback(t *testing.T) {
	if !strings.Contains(RemoteScript, ".docker/run/docker.sock") {
		t.Error("remote script must try the rootless docker socket")
	}
	if !strings.Contains(RemoteScript, "ss -ltnpH") {
		t.Error("remote script must list listening sockets")
	}
}

// TestRemoteScriptRunsInAShell is the regression guard for a bug that unit
// tests with canned output could never catch: the script is a single line, so
// an unquoted '#' marker commented out every command after it and the scan
// silently found nothing. Running it through a real sh is the only way to see
// that. It shells out but touches no network and no ssh.
func TestRemoteScriptRunsInAShell(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a shell")
	}
	out, err := exec.Command("sh", "-c", RemoteScript).Output()
	if err != nil {
		t.Fatalf("the script must never fail: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, markerSS) {
		t.Errorf("ss marker missing from output:\n%s", text)
	}
	if !strings.Contains(text, markerDocker) {
		t.Errorf("docker marker missing from output:\n%s", text)
	}
}

// On a remote where you are not root, ss cannot read the process behind another
// user's socket, so the name-based noise filter sees "" and lets sshd, postfix
// and resolved through. Real scans are dominated by those, so unlabelled
// well-known and ephemeral ports must be filtered by number too.
func TestDiscoverFiltersUnlabelledSystemAndEphemeralPorts(t *testing.T) {
	out := []byte(markerSS + `
LISTEN 0 128 0.0.0.0:22 0.0.0.0:*
LISTEN 0 100 127.0.0.1:25 0.0.0.0:*
LISTEN 0 4096 127.0.0.54:53 0.0.0.0:*
LISTEN 0 4096 100.113.155.105:53592 0.0.0.0:*
LISTEN 0 511 127.0.0.1:3100 0.0.0.0:* users:(("next-server",pid=1,fd=22))
` + markerDocker + "\n")

	ports, err := Discover(context.Background(), fakeRunner{out: out}, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("only the labelled dev server should survive, got %+v", ports)
	}
	if ports[0].Port != 3100 {
		t.Errorf("expected port 3100, got %d", ports[0].Port)
	}
}

func TestDiscoverIncludeAllKeepsUnlabelledPorts(t *testing.T) {
	out := []byte(markerSS + "\nLISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n" + markerDocker + "\n")
	ports, err := Discover(context.Background(), fakeRunner{out: out}, "dev", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("--all must keep unlabelled ports, got %+v", ports)
	}
}

// A labelled port must survive even if its number looks ephemeral: a dev server
// on a high port is exactly what the user wants to reach.
func TestDiscoverKeepsLabelledHighPorts(t *testing.T) {
	out := []byte(markerSS + "\nLISTEN 0 128 127.0.0.1:41234 0.0.0.0:* users:((\"vite\",pid=9,fd=3))\n" + markerDocker + "\n")
	ports, err := Discover(context.Background(), fakeRunner{out: out}, "dev", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(ports) != 1 {
		t.Fatalf("a labelled high port must survive, got %+v", ports)
	}
}
