package discover

import (
	"os"
	"testing"
)

func TestParseSS(t *testing.T) {
	out, err := os.ReadFile("testdata/ss.txt")
	if err != nil {
		t.Fatal(err)
	}
	got := ParseSS(out)

	want := []Socket{
		{Bind: "0.0.0.0", Port: 22, Process: "sshd", PID: 1},
		{Bind: "127.0.0.1", Port: 3100, Process: "next-server (v1", PID: 260486},
		{Bind: "127.0.0.1", Port: 3101, Process: "MainThread", PID: 260639},
		{Bind: "127.0.0.1", Port: 3102, Process: "rootlesskit", PID: 244552},
		{Bind: "127.0.0.1", Port: 3105, Process: "rootlesskit", PID: 244552},
		{Bind: "0.0.0.0", Port: 80, Process: "socat", PID: 266226},
		{Bind: "::", Port: 22, Process: "", PID: 0},
		{Bind: "127.0.0.53%lo", Port: 53, Process: "systemd-resolve", PID: 900},
	}

	if len(got) != len(want) {
		t.Fatalf("got %d sockets, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("socket %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseSSIgnoresGarbage(t *testing.T) {
	got := ParseSS([]byte("not a socket line\n\nLISTEN 0 1\n"))
	if len(got) != 0 {
		t.Fatalf("expected no sockets, got %+v", got)
	}
}
