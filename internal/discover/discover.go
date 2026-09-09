package discover

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const (
	markerSS     = "#portscout:ss"
	markerDocker = "#portscout:docker"
)

// RemoteScript runs on the remote host. It is deliberately POSIX sh and never
// fails: a missing docker must degrade to an ss-only scan, not an error. The
// second docker attempt covers rootless installs, where the socket lives under
// $HOME instead of /run/user/$UID.
const RemoteScript = "echo " + markerSS + "; " +
	"ss -ltnpH 2>/dev/null || true; " +
	"echo " + markerDocker + "; " +
	"{ docker ps --format '{{json .}}' 2>/dev/null " +
	"|| DOCKER_HOST=\"unix://$HOME/.docker/run/docker.sock\" docker ps --format '{{json .}}' 2>/dev/null " +
	"|| true; }"

// noise is the set of processes whose ports are never interesting to forward.
var noise = map[string]bool{
	"sshd":             true,
	"systemd-resolve":  true,
	"systemd-resolved": true,
	"chronyd":          true,
	"chrony":           true,
	"cupsd":            true,
	"avahi-daemon":     true,
	"dnsmasq":          true,
	"rpcbind":          true,
	"postfix":          true,
	"master":           true,
}

// Port is one remote port, after collapsing dual-stack binds and attaching the
// container name when the socket belongs to one.
type Port struct {
	Port      int
	Binds     []string
	Process   string
	Container string
}

// Label is the best human name for the port: the container when there is one,
// because with rootless docker every published port reports the same
// "rootlesskit" process and the process name is useless.
func (p Port) Label() string {
	if p.Container != "" {
		return p.Container
	}
	return p.Process
}

// Runner executes a shell script on a host and returns its stdout.
type Runner interface {
	Run(ctx context.Context, host, script string) ([]byte, error)
}

// SSHRunner runs the script over ssh, inheriting the user's ssh config.
type SSHRunner struct{}

func (SSHRunner) Run(ctx context.Context, host, script string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", host, script)
	out, err := cmd.Output()
	if err != nil {
		// ssh's own diagnostic is far more useful than "exit status 255".
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("ssh %s: %s", host, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ssh %s: %w", host, err)
	}
	return out, nil
}

// Discover runs the remote script and returns the ports worth showing.
func Discover(ctx context.Context, r Runner, host string, includeAll bool) ([]Port, error) {
	out, err := r.Run(ctx, host, RemoteScript)
	if err != nil {
		return nil, err
	}
	ssPart, dockerPart := split(string(out))
	containers := ParseDocker([]byte(dockerPart))

	merged := map[int]*Port{}
	for _, s := range ParseSS([]byte(ssPart)) {
		p, ok := merged[s.Port]
		if !ok {
			p = &Port{Port: s.Port, Process: s.Process, Container: containers[s.Port]}
			merged[s.Port] = p
		}
		p.Binds = append(p.Binds, s.Bind)
	}

	ports := make([]Port, 0, len(merged))
	for _, p := range merged {
		if !includeAll && p.Container == "" && noise[p.Process] {
			continue
		}
		ports = append(ports, *p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports, nil
}

// split cuts the combined stdout into its two sections.
func split(out string) (ssPart, dockerPart string) {
	i := strings.Index(out, markerSS)
	if i >= 0 {
		out = out[i+len(markerSS):]
	}
	j := strings.Index(out, markerDocker)
	if j < 0 {
		return out, ""
	}
	return out[:j], out[j+len(markerDocker):]
}
