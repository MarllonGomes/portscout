package sshmux

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// The stderr strings below are what OpenSSH actually prints. ssh is not
// localised, so matching its English is stable across systems — but not across
// major versions, which is why anything unrecognised still has to be usable.
func TestClassify(t *testing.T) {
	cases := []struct {
		name   string
		stderr string
		err    error
		want   Kind
	}{
		{
			name:   "bind, port taken",
			stderr: "bind [127.0.0.1]:3105: Address already in use",
			want:   KindBind,
		},
		{
			name:   "bind, ssh's own wording",
			stderr: "cannot listen to port: 3105",
			want:   KindBind,
		},
		{
			name:   "bind, the summary line",
			stderr: "Port forwarding failed",
			want:   KindBind,
		},
		{
			name:   "bind, privileged port",
			stderr: "bind [127.0.0.1]:80: Permission denied",
			want:   KindBind,
		},
		{
			name:   "auth refused",
			stderr: "m@dev: Permission denied (publickey).",
			want:   KindAuth,
		},
		{
			name:   "host key changed",
			stderr: "@@@ WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED! @@@",
			want:   KindHostKey,
		},
		{
			name:   "host key unverified",
			stderr: "Host key verification failed.",
			want:   KindHostKey,
		},
		{
			name:   "no master behind the socket",
			stderr: "Control socket connect(/run/user/1000/portscout/a.sock): No such file or directory",
			want:   KindNoMaster,
		},
		{
			name:   "remote unreachable",
			stderr: "ssh: connect to host dev port 22: Connection refused",
			want:   KindRefused,
		},
		{
			name:   "our own deadline",
			stderr: "",
			err:    context.DeadlineExceeded,
			want:   KindTimeout,
		},
		{
			name:   "anything else is still displayable",
			stderr: "some future openssh message",
			want:   KindUnknown,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Classify("forward", c.stderr, c.err)
			if got.Kind != c.want {
				t.Errorf("Kind = %v, want %v", got.Kind, c.want)
			}
			if c.stderr != "" && !strings.Contains(got.Raw, strings.TrimSpace(c.stderr)) {
				t.Errorf("ssh's own words must survive for the detail line, got Raw=%q", got.Raw)
			}
			if got.Op != "forward" {
				t.Errorf("Op = %q, want %q", got.Op, "forward")
			}
			if got.Error() == "" {
				t.Error("an error must always render as something")
			}
		})
	}
}

// A bind failure is the user's to fix — retrying it once a second just generates
// noise — while a dropped master heals on its own.
func TestKindRetryable(t *testing.T) {
	for kind, want := range map[Kind]bool{
		KindBind:     false,
		KindAuth:     false,
		KindHostKey:  false,
		KindNoMaster: true,
		KindRefused:  true,
		KindTimeout:  true,
		KindUnknown:  true,
	} {
		if got := kind.Retryable(); got != want {
			t.Errorf("%v.Retryable() = %v, want %v", kind, got, want)
		}
	}
}

func TestErrorUnwraps(t *testing.T) {
	base := errors.New("boom")
	e := Classify("check", "", base)
	if !errors.Is(e, base) {
		t.Error("the underlying error must stay reachable through errors.Is")
	}
}

// Classify is only ever called on a failure, so a nil result would force every
// caller to nil-check twice.
func TestClassifyNeverReturnsNil(t *testing.T) {
	if Classify("forward", "", nil) == nil {
		t.Fatal("Classify must always return an error value")
	}
}
