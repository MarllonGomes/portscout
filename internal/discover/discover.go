package discover

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

// The markers must not start with '#': the script is a single line, so a '#'
// would comment out everything after it and the scan would silently return
// nothing.
const (
	markerSS      = "portscout-section:ss"
	markerDocker  = "portscout-section:docker"
	markerCmdline = "portscout-section:cmdline"
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
	"|| true; }; " +
	"echo " + markerCmdline + "; " +
	// ss only ever reports the first 15 characters of a process name; the full
	// one has to come from /proc. sed, not grep -oP, because -P is a GNU
	// extension the remote sh may not have.
	"ss -ltnpH 2>/dev/null | sed -n 's/.*pid=\\([0-9]*\\).*/\\1/p' | sort -u | " +
	"while read p; do printf '%s\\t%s\\n' \"$p\" \"$(tr '\\0' ' ' < /proc/$p/cmdline 2>/dev/null)\"; done 2>/dev/null || true"

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

// noisePorts are well-known service ports that are never worth forwarding.
// They are filtered by number because ss running as an unprivileged user cannot
// read the process behind another user's socket, which is exactly the case for
// every system daemon on a remote box.
var noisePorts = map[int]bool{
	22: true, 25: true, 53: true, 111: true, 123: true,
	631: true, 5353: true, 5355: true,
}

// firstEphemeral is the bottom of Linux's default dynamic port range. An
// unlabelled socket up there is a transient client port, not a service.
const firstEphemeral = 32768

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
	ssPart, dockerPart, cmdlinePart := split(string(out))
	containers := ParseDocker([]byte(dockerPart))
	cmdlines := ParseCmdlines([]byte(cmdlinePart))

	merged := map[int]*Port{}
	for _, s := range ParseSS([]byte(ssPart)) {
		p, ok := merged[s.Port]
		if !ok {
			p = &Port{
				Port:      s.Port,
				Process:   untruncateProcess(s.Process, cmdlines[s.PID]),
				Container: containers[s.Port],
			}
			merged[s.Port] = p
		}
		p.Binds = append(p.Binds, s.Bind)
	}

	ports := make([]Port, 0, len(merged))
	for _, p := range merged {
		if !includeAll && p.IsNoise() {
			continue
		}
		ports = append(ports, *p)
	}
	sort.Slice(ports, func(i, j int) bool { return ports[i].Port < ports[j].Port })
	return ports, nil
}

// IsNoise reports whether a port is infrastructure the user never wants to
// forward. A port with a label is always kept: a dev server on a high port is
// precisely what the user is looking for.
//
// Exported so the dashboard can filter at display time instead of at discovery
// time: a row the user already chose must stay visible even when it looks like
// noise, and toggling "show everything" must not need a fresh scan.
func (p Port) IsNoise() bool {
	if p.Container != "" {
		return false
	}
	if p.Process != "" {
		return noise[p.Process]
	}
	return noisePorts[p.Port] || p.Port >= firstEphemeral
}

// split cuts the combined stdout into its sections. A missing section yields an
// empty string: an older remote script, or one whose docker call produced
// nothing, must still give a usable scan.
func split(out string) (ssPart, dockerPart, cmdlinePart string) {
	if i := strings.Index(out, markerSS); i >= 0 {
		out = out[i+len(markerSS):]
	}
	j := strings.Index(out, markerDocker)
	if j < 0 {
		return out, "", ""
	}
	ssPart, rest := out[:j], out[j+len(markerDocker):]
	k := strings.Index(rest, markerCmdline)
	if k < 0 {
		return ssPart, rest, ""
	}
	return ssPart, rest[:k], rest[k+len(markerCmdline):]
}
