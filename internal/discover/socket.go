// Package discover turns the raw output of remote commands into labelled ports.
package discover

import (
	"regexp"
	"strconv"
	"strings"
)

// Socket is one listening TCP socket reported by ss.
type Socket struct {
	Bind    string // "127.0.0.1", "0.0.0.0", "::"
	Port    int
	Process string // empty when ss could not read the process
	PID     int    // 0 when ss could not read the process
}

// commLimit is TASK_COMM_LEN-1: the kernel stores a process name in 16 bytes
// including the NUL, so ss reports at most 15 characters. "next-server (v1" is
// a full name that hit the ceiling, not a name that is actually that short.
const commLimit = 15

// ss prints the process as users:(("name",pid=N,fd=M)). The name may contain
// spaces and parentheses, so anchor on the quotes rather than on the parens.
var ssProcess = regexp.MustCompile(`\(\("([^"]*)",pid=(\d+)`)

// ParseSS reads `ss -ltnpH` output. Malformed lines are skipped rather than
// failing the whole scan: one odd line must not cost the user every port.
func ParseSS(out []byte) []Socket {
	var sockets []Socket
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		bind, port, ok := splitHostPort(fields[3])
		if !ok {
			continue
		}
		process, pid := "", 0
		if m := ssProcess.FindStringSubmatch(line); m != nil {
			process = m[1]
			pid, _ = strconv.Atoi(m[2])
		}
		sockets = append(sockets, Socket{Bind: bind, Port: port, Process: process, PID: pid})
	}
	return sockets
}

// splitHostPort handles "127.0.0.1:3100", "[::]:22" and "*:22".
func splitHostPort(addr string) (string, int, bool) {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return "", 0, false
	}
	host, portText := addr[:i], addr[i+1:]
	port, err := strconv.Atoi(portText)
	if err != nil {
		return "", 0, false
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if host == "*" {
		host = "0.0.0.0"
	}
	return host, port, true
}

// untruncateProcess recovers a process name the kernel cut off at commLimit,
// using the full command line of the same pid.
//
// The cmdline must actually continue the comm to be used: a wrapper whose
// argv[0] is an unrelated path would otherwise replace a short-but-accurate
// name with a long and wrong one.
func untruncateProcess(comm, cmdline string) string {
	cmdline = strings.TrimSpace(cmdline)
	if len(comm) < commLimit || cmdline == "" {
		return comm
	}
	if !strings.HasPrefix(cmdline, comm) {
		return comm
	}
	return cmdline
}
