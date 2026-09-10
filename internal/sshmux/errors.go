package sshmux

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Kind is what went wrong, reduced to the handful of cases the supervisor
// actually decides differently on.
type Kind int

const (
	KindUnknown  Kind = iota
	KindBind          // the local port could not be listened on
	KindAuth          // the server refused our credentials
	KindHostKey       // unknown or changed host key
	KindNoMaster      // nothing is listening on the control socket
	KindRefused       // the remote host could not be reached
	KindTimeout       // our own deadline fired
)

func (k Kind) String() string {
	switch k {
	case KindBind:
		return "bind"
	case KindAuth:
		return "auth"
	case KindHostKey:
		return "hostkey"
	case KindNoMaster:
		return "nomaster"
	case KindRefused:
		return "refused"
	case KindTimeout:
		return "timeout"
	default:
		return "unknown"
	}
}

// Retryable reports whether trying the same thing again could ever succeed
// without the user doing something first.
//
// A busy local port and a rejected key do not heal on their own, and retrying
// them on a timer produces a stream of identical errors that buries the one
// message the user needs to read. A dropped connection does heal, so it is
// retried with backoff.
func (k Kind) Retryable() bool {
	switch k {
	case KindBind, KindAuth, KindHostKey:
		return false
	default:
		return true
	}
}

// Error is a classified ssh failure. Raw is always kept: the classification
// drives behaviour, but ssh's own words are what tell the user what to do.
type Error struct {
	Op   string // "master", "forward", "cancel", "check"
	Kind Kind
	Raw  string
	err  error
}

func (e *Error) Error() string {
	switch {
	case e.Raw != "" && e.err != nil:
		return fmt.Sprintf("ssh %s (%s): %s: %v", e.Op, e.Kind, e.Raw, e.err)
	case e.Raw != "":
		return fmt.Sprintf("ssh %s (%s): %s", e.Op, e.Kind, e.Raw)
	case e.err != nil:
		return fmt.Sprintf("ssh %s (%s): %v", e.Op, e.Kind, e.err)
	default:
		return fmt.Sprintf("ssh %s (%s)", e.Op, e.Kind)
	}
}

func (e *Error) Unwrap() error { return e.err }

// classifiers are matched in order; the first hit wins. Bind comes before auth
// because a privileged-port bind failure says "Permission denied" too, and it is
// a bind problem, not a credentials problem.
var classifiers = []struct {
	kind    Kind
	needles []string
}{
	{KindBind, []string{
		"cannot listen to port",
		"address already in use",
		"port forwarding failed",
		// ssh prints "bind [127.0.0.1]:80: Permission denied" for a privileged
		// port. That is a bind problem, not a credentials one, which is why this
		// group has to be matched before KindAuth.
		"bind [",
		"bind:",
	}},
	{KindHostKey, []string{
		"host key verification failed",
		"remote host identification has changed",
		"no matching host key type",
	}},
	{KindNoMaster, []string{
		"control socket connect",
		"no such file or directory",
		"not a multiplexing master",
	}},
	{KindAuth, []string{
		"permission denied",
		"too many authentication failures",
		"no supported methods remain",
	}},
	{KindRefused, []string{
		"connection refused",
		"connection timed out",
		"could not resolve hostname",
		"network is unreachable",
		"no route to host",
	}},
}

// Classify maps ssh's stderr onto a Kind. An unrecognised message falls through
// to KindUnknown, which is retryable and still renders — a future OpenSSH
// rewording must degrade to "we don't know", never to a wrong decision.
func Classify(op, stderr string, err error) *Error {
	raw := strings.TrimSpace(stderr)
	e := &Error{Op: op, Raw: raw, err: err, Kind: KindUnknown}

	if err != nil && (errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)) {
		e.Kind = KindTimeout
		return e
	}

	// A bind failure prints the detail line and the summary separately, so match
	// against the whole thing rather than the last line.
	hay := strings.ToLower(raw)
	for _, c := range classifiers {
		for _, needle := range c.needles {
			if strings.Contains(hay, needle) {
				e.Kind = c.kind
				return e
			}
		}
	}
	return e
}
