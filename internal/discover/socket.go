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
}

// ss prints the process as users:(("name",pid=N,fd=M)). The name may contain
// spaces and parentheses, so anchor on the quotes rather than on the parens.
var ssProcess = regexp.MustCompile(`\(\("([^"]*)"`)

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
		process := ""
		if m := ssProcess.FindStringSubmatch(line); m != nil {
			process = m[1]
		}
		sockets = append(sockets, Socket{Bind: bind, Port: port, Process: process})
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
