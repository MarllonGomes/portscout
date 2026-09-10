// Package sshmux drives one OpenSSH ControlMaster: the forwards and the port
// scan all ride a single authenticated connection, so toggling a tunnel costs a
// unix-socket round trip instead of a full SSH handshake.
//
// Everything here delegates to the ssh(1) binary rather than speaking the
// protocol in Go. That is deliberate: a Go SSH client would have to reimplement
// ssh_config — Match blocks, tokens, IdentityFile expansion, IdentitiesOnly,
// ProxyCommand, known_hosts — and getting any of it subtly wrong breaks hosts
// that already work in the user's terminal.
package sshmux

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxSunPath is the smallest sun_path across the platforms we ship to: 108 on
// Linux, 104 on macOS. Overflowing it fails at bind time with an error that
// points nowhere near the cause, so the check happens up front.
const maxSunPath = 104

// Forward is one local forward, in the shape ssh(1) needs.
type Forward struct {
	LocalPort  int
	RemoteHost string // "127.0.0.1" by default; "::1" when the remote binds v6 only
	RemotePort int
}

// Spec renders the -L argument.
//
// The local side is always the literal 127.0.0.1, never "localhost" and never
// 0.0.0.0. "localhost" makes ssh bind both address families and report success
// if either bind worked, so a port that is free on v4 and taken on v6 would look
// forwarded while half the listener belongs to someone else.
//
// Forward and cancel must produce byte-identical strings: ssh matches a cancel
// against the pair it was given, so any drift leaks the listener.
func (f Forward) Spec() string {
	remote := f.RemoteHost
	if remote == "" {
		remote = "127.0.0.1"
	}
	// ssh(1): IPv6 addresses are specified by enclosing them in square brackets.
	if strings.Contains(remote, ":") && !strings.HasPrefix(remote, "[") {
		remote = "[" + remote + "]"
	}
	return fmt.Sprintf("127.0.0.1:%d:%s:%d", f.LocalPort, remote, f.RemotePort)
}

// masterArgs builds the long-lived multiplexing connection.
//
// -M -S is passed explicitly so we always own our socket: whatever ControlMaster
// or ControlPath the user has in ssh_config, we can neither hijack their
// long-lived connection nor be broken by it.
func masterArgs(host, socket string) []string {
	return []string{
		"-M", "-N", "-S", socket,
		// The dashboard owns the tunnels for exactly as long as it runs, and
		// this is what enforces it: the master cannot outlive us, even if we are
		// killed before any cleanup can run.
		"-o", "ControlPersist=no",
		"-o", "BatchMode=yes",
		// A wedged link makes ssh exit on its own within ~45s, which is how the
		// supervisor learns the connection died without polling for it.
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		host,
	}
}

// controlArgs builds one -O command against a running master. fwd is required
// for "forward" and "cancel", and ignored otherwise.
func controlArgs(host, socket, op string, fwd *Forward) []string {
	args := []string{"-S", socket, "-O", op}
	if fwd != nil {
		args = append(args, "-L", fwd.Spec())
	}
	return append(args, host)
}

// runnerArgs runs the discovery script over the master.
//
// ControlMaster=no keeps this from negotiating a master of its own; without it
// we could end up owning two connections to the same host.
func runnerArgs(host, socket, script string) []string {
	return []string{"-S", socket, "-o", "ControlMaster=no", "-o", "BatchMode=yes", host, script}
}

// SocketPath is the control socket for a host: deterministic, so a master left
// behind by a killed run can be found and cleaned up on the next launch.
//
// The host is hashed rather than used verbatim. Destinations like "user@host" or
// an IPv6 literal are not filesystem-safe, and the path budget is far too small
// to spend on a readable name.
func SocketPath(host string) (string, error) {
	sum := sha256.Sum256([]byte(host))
	name := hex.EncodeToString(sum[:])[:12] + ".sock"

	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	path := filepath.Join(dir, "portscout", name)
	if len(path) > maxSunPath {
		return "", fmt.Errorf("caminho do socket ssh longo demais (%d bytes, limite %d): %s",
			len(path), maxSunPath, path)
	}
	return path, nil
}
