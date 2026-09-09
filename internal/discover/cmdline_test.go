package discover

import "testing"

// O kernel corta comm em 15 chars (TASK_COMM_LEN-1), então o ss reporta
// "next-server (v1" para um processo que se chama "next-server (v16.3.4)".
func TestParseCmdlines(t *testing.T) {
	out := []byte("46893\tnext-server (v16.3.4) \n46644\t/home/m/.nvm/bin/node --require /x \n1403\trootlesskit --state-dir=/run \n")
	got := ParseCmdlines(out)
	if got[46893] != "next-server (v16.3.4)" {
		t.Errorf("cmdline do 46893 = %q", got[46893])
	}
	if len(got) != 3 {
		t.Errorf("esperava 3 pids, got %d: %+v", len(got), got)
	}
}

func TestParseSSCapturesPID(t *testing.T) {
	out := []byte(`LISTEN 0 511 127.0.0.1:3100 0.0.0.0:* users:(("next-server (v1",pid=46893,fd=22))`)
	got := ParseSS(out)
	if len(got) != 1 {
		t.Fatalf("esperava 1 socket, got %d", len(got))
	}
	if got[0].PID != 46893 {
		t.Errorf("PID = %d, esperava 46893", got[0].PID)
	}
}

// Um comm truncado deve ser estendido pelo cmdline, mas só quando o cmdline
// realmente continua o comm — senão trocaríamos um nome ruim por um pior.
func TestUntruncateProcess(t *testing.T) {
	cases := []struct {
		name, comm, cmdline, want string
	}{
		{"estende o truncado", "next-server (v1", "next-server (v16.3.4)", "next-server (v16.3.4)"},
		{"nao mexe no que nao truncou", "MainThread", "/home/m/.nvm/bin/node --require /x", "MainThread"},
		{"cmdline que nao continua o comm fica de fora", "rootlesskit-abc", "/usr/bin/outra-coisa --x", "rootlesskit-abc"},
		{"sem cmdline mantem o comm", "next-server (v1", "", "next-server (v1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := untruncateProcess(c.comm, c.cmdline); got != c.want {
				t.Errorf("untruncateProcess(%q, %q) = %q, want %q", c.comm, c.cmdline, got, c.want)
			}
		})
	}
}
