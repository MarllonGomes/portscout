package discover

import (
	"context"
	"errors"
	"os"
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
