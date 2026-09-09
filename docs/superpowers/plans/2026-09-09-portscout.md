# portscout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Um binário Go que descobre portas em LISTEN numa máquina remota via SSH e escreve as entradas correspondentes no YAML do tunnel9, sem nunca abrir túneis nem tocar nas entradas manuais do usuário.

**Architecture:** Três pacotes internos com fronteiras nítidas — `discover` (executa um snippet POSIX no remoto e transforma a saída de `ss` e `docker ps` em portas rotuladas), `plan` (decide alias e resolve colisão de porta local), `tunnel9` (lê, faz merge e grava o YAML preservando comentários via `yaml.Node`). O `cmd/portscout` só amarra os três. A execução remota fica atrás de uma interface `Runner`, então nenhum teste abre SSH.

**Tech Stack:** Go 1.27, `gopkg.in/yaml.v3`, biblioteca padrão para o resto. Sem framework de CLI — `flag` da stdlib basta para três subcomandos.

**Spec:** `docs/superpowers/specs/2026-09-09-portscout-design.md`

## Global Constraints

- Go 1.27.1. O toolchain nesta VPS está em `~/.local/go/bin/go` e **não** está no PATH: todo comando `go` deve ser invocado como `~/.local/go/bin/go` ou com `export PATH="$HOME/.local/go/bin:$PATH"` no início do shell.
- Módulo: `github.com/MarllonGomes/portscout`.
- Dependência externa única permitida: `gopkg.in/yaml.v3`. Nada além disso.
- `portscout` **nunca** executa `ssh -L`, `socat` ou qualquer coisa que abra um túnel. Só lê o remoto e escreve YAML.
- O programa só é dono de entradas cuja `tag` seja igual à tag gerenciada (padrão `auto`). Qualquer outra entrada do arquivo é intocável.
- Nunca gravar o YAML se o parse falhar. Escrita sempre atômica (tmp + `os.Rename`).
- Todo teste roda offline. Nenhum teste pode invocar `ssh`, `docker` ou rede.
- Mensagens de usuário em português; identificadores, comentários de código e mensagens de commit em inglês (padrão do repo).

---

### Task 1: Esqueleto do módulo e parsing de `ss`

**Files:**
- Create: `go.mod`
- Create: `internal/discover/socket.go`
- Test: `internal/discover/socket_test.go`
- Create: `internal/discover/testdata/ss.txt`

**Interfaces:**
- Consumes: nada (primeira task).
- Produces: `discover.Socket{Bind string; Port int; Process string}` e `discover.ParseSS(out []byte) []Socket`. As tasks 2 e 3 consomem ambos.

- [ ] **Step 1: Inicializar o módulo**

```bash
cd ~/projects/portscout
export PATH="$HOME/.local/go/bin:$PATH"
go mod init github.com/MarllonGomes/portscout
```

- [ ] **Step 2: Criar a fixture**

Criar `internal/discover/testdata/ss.txt` com exatamente este conteúdo (capturado da VPS real, inclui o caso rootless, um processo com espaço e parêntese no nome, uma linha sem informação de processo e IPv6):

```
LISTEN 0      128                0.0.0.0:22         0.0.0.0:*    users:(("sshd",pid=1,fd=3))
LISTEN 0      511              127.0.0.1:3100       0.0.0.0:*    users:(("next-server (v1",pid=260486,fd=22))
LISTEN 0      511              127.0.0.1:3101       0.0.0.0:*    users:(("MainThread",pid=260639,fd=28))
LISTEN 0      4096             127.0.0.1:3102       0.0.0.0:*    users:(("rootlesskit",pid=244552,fd=24))
LISTEN 0      4096             127.0.0.1:3105       0.0.0.0:*    users:(("rootlesskit",pid=244552,fd=23))
LISTEN 0      5                  0.0.0.0:80         0.0.0.0:*    users:(("socat",pid=266226,fd=5))
LISTEN 0      128                   [::]:22            [::]:*
LISTEN 0      4096             127.0.0.53%lo:53     0.0.0.0:*    users:(("systemd-resolve",pid=900,fd=14))
```

- [ ] **Step 3: Escrever o teste que falha**

Criar `internal/discover/socket_test.go`:

```go
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
		{Bind: "0.0.0.0", Port: 22, Process: "sshd"},
		{Bind: "127.0.0.1", Port: 3100, Process: "next-server (v1"},
		{Bind: "127.0.0.1", Port: 3101, Process: "MainThread"},
		{Bind: "127.0.0.1", Port: 3102, Process: "rootlesskit"},
		{Bind: "127.0.0.1", Port: 3105, Process: "rootlesskit"},
		{Bind: "0.0.0.0", Port: 80, Process: "socat"},
		{Bind: "::", Port: 22, Process: ""},
		{Bind: "127.0.0.53%lo", Port: 53, Process: "systemd-resolve"},
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
```

- [ ] **Step 4: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./internal/discover/ -run TestParseSS -v`
Expected: FAIL — `undefined: Socket` e `undefined: ParseSS`.

- [ ] **Step 5: Implementar o mínimo**

Criar `internal/discover/socket.go`:

```go
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
```

- [ ] **Step 6: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/discover/ -v`
Expected: PASS nos dois testes.

- [ ] **Step 7: Commit**

```bash
cd ~/projects/portscout
git add go.mod internal/discover/
git commit -m "feat(discover): parse ss listening sockets"
```

---

### Task 2: Mapear portas de container a partir do `docker ps`

**Files:**
- Create: `internal/discover/docker.go`
- Test: `internal/discover/docker_test.go`
- Create: `internal/discover/testdata/docker.json`

**Interfaces:**
- Consumes: nada da task 1 (parsing independente).
- Produces: `discover.ParseDocker(out []byte) map[int]string` — porta publicada no host → nome do container. A task 3 usa isso para rotular sockets cujo processo é `rootlesskit`.

- [ ] **Step 1: Criar a fixture**

Criar `internal/discover/testdata/docker.json` (uma linha JSON por container, formato de `docker ps --format '{{json .}}'`; cobre publicação em loopback, publicação dupla IPv4/IPv6 e container sem porta publicada):

```
{"ID":"a1","Image":"postgres:18.3-alpine","Names":"couple-community-postgres-1","Ports":"127.0.0.1:3102->5432/tcp"}
{"ID":"b2","Image":"redis:8.6.1-alpine","Names":"couple-community-redis-1","Ports":"127.0.0.1:3103->6379/tcp"}
{"ID":"c3","Image":"axllent/mailpit:v1.31.0","Names":"couple-community-mailpit-1","Ports":"127.0.0.1:3104->1025/tcp, 127.0.0.1:3105->8025/tcp"}
{"ID":"d4","Image":"nginx","Names":"web","Ports":"0.0.0.0:8080->80/tcp, :::8080->80/tcp"}
{"ID":"e5","Image":"busybox","Names":"idle","Ports":""}
```

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/discover/docker_test.go`:

```go
package discover

import (
	"os"
	"testing"
)

func TestParseDocker(t *testing.T) {
	out, err := os.ReadFile("testdata/docker.json")
	if err != nil {
		t.Fatal(err)
	}
	got := ParseDocker(out)

	want := map[int]string{
		3102: "couple-community-postgres-1",
		3103: "couple-community-redis-1",
		3104: "couple-community-mailpit-1",
		3105: "couple-community-mailpit-1",
		8080: "web",
	}

	if len(got) != len(want) {
		t.Fatalf("got %d mappings, want %d: %+v", len(got), len(want), got)
	}
	for port, name := range want {
		if got[port] != name {
			t.Errorf("port %d: got %q, want %q", port, got[port], name)
		}
	}
}

func TestParseDockerHandlesEmptyAndGarbage(t *testing.T) {
	if got := ParseDocker(nil); len(got) != 0 {
		t.Errorf("nil input: got %+v", got)
	}
	if got := ParseDocker([]byte("not json\n")); len(got) != 0 {
		t.Errorf("garbage input: got %+v", got)
	}
}
```

- [ ] **Step 3: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./internal/discover/ -run TestParseDocker -v`
Expected: FAIL — `undefined: ParseDocker`.

- [ ] **Step 4: Implementar o mínimo**

Criar `internal/discover/docker.go`:

```go
package discover

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
)

// The host port is always the number immediately before "->", which sidesteps
// having to parse the address in front of it (":::8080->80/tcp" and
// "127.0.0.1:3102->5432/tcp" both work). An unpublished "5432/tcp" has no
// arrow and is correctly ignored.
var dockerHostPort = regexp.MustCompile(`(\d+)->\d+/tcp`)

// ParseDocker reads `docker ps --format '{{json .}}'` output and maps each
// published host port to its container name. Unparseable lines are skipped:
// losing one container's label must not fail the scan.
func ParseDocker(out []byte) map[int]string {
	byPort := map[int]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var row struct {
			Names string `json:"Names"`
			Ports string `json:"Ports"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			continue
		}
		if row.Names == "" {
			continue
		}
		for _, m := range dockerHostPort.FindAllStringSubmatch(row.Ports, -1) {
			port, err := strconv.Atoi(m[1])
			if err != nil {
				continue
			}
			byPort[port] = row.Names
		}
	}
	return byPort
}
```

- [ ] **Step 5: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/discover/ -v`
Expected: PASS em todos os testes do pacote.

- [ ] **Step 6: Commit**

```bash
cd ~/projects/portscout
git add internal/discover/
git commit -m "feat(discover): map published host ports to container names"
```

---

### Task 3: Runner remoto, filtro de ruído e a função `Discover`

**Files:**
- Create: `internal/discover/discover.go`
- Test: `internal/discover/discover_test.go`

**Interfaces:**
- Consumes: `ParseSS`, `ParseDocker`, `Socket` das tasks 1-2.
- Produces:
  - `discover.Port{Port int; Binds []string; Process string; Container string}` e `(Port).Label() string`
  - `discover.Runner` interface com `Run(ctx context.Context, host, script string) ([]byte, error)`
  - `discover.SSHRunner` implementando `Runner`
  - `discover.Discover(ctx context.Context, r Runner, host string, includeAll bool) ([]Port, error)`
  - `discover.RemoteScript` (string, o snippet POSIX)

  A task 5 consome `Port` e `Label()`. A task 7 consome `Discover` e `SSHRunner`.

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/discover/discover_test.go`:

```go
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

	// sshd and systemd-resolve are noise and must be gone.
	if _, ok := byPort[22]; ok {
		t.Error("port 22 (sshd) should be filtered as noise")
	}
	if _, ok := byPort[53]; ok {
		t.Error("port 53 (systemd-resolve) should be filtered as noise")
	}

	// A rootless container port must be labelled by container, not "rootlesskit".
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

	// A plain host process keeps its process name as the label.
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
```

- [ ] **Step 2: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./internal/discover/ -run TestDiscover -v`
Expected: FAIL — `undefined: Discover`, `undefined: markerSS`, `undefined: Port`.

- [ ] **Step 3: Implementar o mínimo**

Criar `internal/discover/discover.go`:

```go
package discover

import (
	"context"
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
	"sshd":            true,
	"systemd-resolve": true,
	"systemd-resolved": true,
	"chronyd":         true,
	"chrony":          true,
	"cupsd":           true,
	"avahi-daemon":    true,
	"dnsmasq":         true,
	"rpcbind":         true,
	"postfix":         true,
	"master":          true,
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
		var ee *exec.ExitError
		if errorsAs(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("ssh %s: %s", host, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("ssh %s: %w", host, err)
	}
	return out, nil
}

// errorsAs is a tiny shim so the import list stays minimal.
func errorsAs(err error, target **exec.ExitError) bool {
	e, ok := err.(*exec.ExitError)
	if ok {
		*target = e
	}
	return ok
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

	var ports []Port
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
```

- [ ] **Step 4: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/discover/ -v`
Expected: PASS em todos os testes do pacote.

- [ ] **Step 5: Commit**

```bash
cd ~/projects/portscout
git add internal/discover/
git commit -m "feat(discover): remote script, dual-stack collapse and noise filter"
```

---

### Task 4: Ler o YAML do tunnel9 preservando comentários

**Files:**
- Create: `internal/tunnel9/config.go`
- Test: `internal/tunnel9/config_test.go`
- Create: `internal/tunnel9/testdata/config.yaml`

**Interfaces:**
- Consumes: nada.
- Produces:
  - `tunnel9.Entry{Host, Alias, User, Tag string; LocalPort, RemotePort int}`
  - `tunnel9.Config` com `Load(path string) (*Config, error)`, `(*Config).Entries() []Entry`, `(*Config).Save(path string) error`
  - `tunnel9.DefaultPath() string`

  A task 6 estende `Config` com `Merge`. A task 7 usa `Load`, `Save` e `DefaultPath`.

- [ ] **Step 1: Criar a fixture**

Criar `internal/tunnel9/testdata/config.yaml` — repare no comentário e na entrada manual sem tag, que os testes exigem preservar:

```yaml
# túneis do marllon — não mexer nas entradas sem tag
tunnels:
  - host: "prod-db.example.com"
    alias: "prod-db"
    user: "dbuser"
    local_port: 15432
    remote_port: 5432
  - host: "dev"
    alias: "postgres antigo"
    user: "m"
    local_port: 4102
    remote_port: 3102
    tag: "auto"
```

- [ ] **Step 2: Escrever o teste que falha**

Criar `internal/tunnel9/config_test.go`:

```go
package tunnel9

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func loadFixture(t *testing.T) (*Config, string) {
	t.Helper()
	src, err := os.ReadFile("testdata/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg, path
}

func TestLoadEntries(t *testing.T) {
	cfg, _ := loadFixture(t)
	got := cfg.Entries()
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Host != "prod-db.example.com" || got[0].LocalPort != 15432 || got[0].Tag != "" {
		t.Errorf("entry 0: %+v", got[0])
	}
	if got[1].Host != "dev" || got[1].RemotePort != 3102 || got[1].Tag != "auto" {
		t.Errorf("entry 1: %+v", got[1])
	}
}

func TestSaveRoundTripPreservesComments(t *testing.T) {
	cfg, path := loadFixture(t)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "não mexer nas entradas sem tag") {
		t.Errorf("comment was lost:\n%s", out)
	}
	if !strings.Contains(string(out), "prod-db.example.com") {
		t.Errorf("manual entry was lost:\n%s", out)
	}
}

func TestLoadMissingFileStartsEmpty(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "absent.yaml"))
	if err != nil {
		t.Fatalf("a missing config must start empty, got %v", err)
	}
	if len(cfg.Entries()) != 0 {
		t.Errorf("expected no entries, got %+v", cfg.Entries())
	}
}

func TestLoadInvalidYAMLFails(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.yaml")
	if err := os.WriteFile(path, []byte("tunnels: [unclosed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected a parse error")
	}
}
```

- [ ] **Step 3: Rodar o teste e confirmar que falha**

```bash
cd ~/projects/portscout
export PATH="$HOME/.local/go/bin:$PATH"
go get gopkg.in/yaml.v3
go test ./internal/tunnel9/ -v
```
Expected: FAIL — `undefined: Load`, `undefined: Config`.

- [ ] **Step 4: Implementar o mínimo**

Criar `internal/tunnel9/config.go`:

```go
// Package tunnel9 reads and writes the YAML config of the tunnel9 TUI.
package tunnel9

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// Entry is one tunnel9 tunnel.
type Entry struct {
	Host       string
	Alias      string
	User       string
	LocalPort  int
	RemotePort int
	Tag        string
}

// Config wraps the parsed document. The whole file is kept as a yaml.Node so
// that comments, key order and fields portscout does not know about survive a
// round trip — the user edits this file by hand and in the tunnel9 TUI.
type Config struct {
	doc     *yaml.Node
	tunnels *yaml.Node // the sequence node under "tunnels"
}

// DefaultPath mirrors tunnel9's own default location.
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "config.yaml"
	}
	return filepath.Join(home, ".local", "state", "tunnel9", "config.yaml")
}

// Load reads the config. A missing file is not an error: it yields an empty
// config that Save will create.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return newEmpty(), nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) == 0 {
		return newEmpty(), nil
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg := &Config{doc: &doc}
	cfg.tunnels = findTunnels(&doc)
	if cfg.tunnels == nil {
		return nil, fmt.Errorf("%s: no \"tunnels\" list found", path)
	}
	return cfg, nil
}

func newEmpty() *Config {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tunnels"},
		seq,
	}}
	return &Config{
		doc:     &yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{root}},
		tunnels: seq,
	}
}

func findTunnels(doc *yaml.Node) *yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "tunnels" {
			v := root.Content[i+1]
			if v.Kind == yaml.SequenceNode {
				return v
			}
			// An empty "tunnels:" parses as a null scalar; make it a sequence.
			v.Kind = yaml.SequenceNode
			v.Tag = "!!seq"
			v.Value = ""
			return v
		}
	}
	return nil
}

// Entries returns the tunnels in file order.
func (c *Config) Entries() []Entry {
	var out []Entry
	for _, n := range c.tunnels.Content {
		if n.Kind != yaml.MappingNode {
			continue
		}
		out = append(out, Entry{
			Host:       mapGet(n, "host"),
			Alias:      mapGet(n, "alias"),
			User:       mapGet(n, "user"),
			LocalPort:  mapGetInt(n, "local_port"),
			RemotePort: mapGetInt(n, "remote_port"),
			Tag:        mapGet(n, "tag"),
		})
	}
	return out
}

// Save writes the document atomically: a crash mid-write must never leave the
// user with a truncated tunnel list.
func (c *Config) Save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := yaml.Marshal(c.doc)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".portscout-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func mapGet(n *yaml.Node, key string) string {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1].Value
		}
	}
	return ""
}

func mapGetInt(n *yaml.Node, key string) int {
	v, err := strconv.Atoi(mapGet(n, key))
	if err != nil {
		return 0
	}
	return v
}
```

- [ ] **Step 5: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/tunnel9/ -v`
Expected: PASS nos quatro testes.

- [ ] **Step 6: Commit**

```bash
cd ~/projects/portscout
git add go.mod go.sum internal/tunnel9/
git commit -m "feat(tunnel9): load and save config preserving comments"
```

---

### Task 5: Alias e resolução de colisão de porta local

**Files:**
- Create: `internal/plan/plan.go`
- Test: `internal/plan/plan_test.go`

**Interfaces:**
- Consumes: `discover.Port` da task 3.
- Produces:
  - `plan.Desired{RemotePort int; Alias string}`
  - `plan.Assignment{RemotePort, LocalPort int; Alias string; Remapped bool}`
  - `plan.Build(ports []discover.Port, taken map[int]bool, free func(int) bool) []Assignment`

  A task 6 consome `Assignment`. A task 7 imprime `Remapped`.

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/plan/plan_test.go`:

```go
package plan

import (
	"testing"

	"github.com/MarllonGomes/portscout/internal/discover"
)

func allFree(int) bool { return true }

func TestBuildDefaultsLocalToRemote(t *testing.T) {
	ports := []discover.Port{
		{Port: 3100, Process: "next-server (v1"},
		{Port: 3102, Process: "rootlesskit", Container: "postgres-1"},
	}
	got := Build(ports, map[int]bool{}, allFree)

	if len(got) != 2 {
		t.Fatalf("got %d assignments, want 2", len(got))
	}
	if got[0].LocalPort != 3100 || got[0].Remapped {
		t.Errorf("3100 should map to itself: %+v", got[0])
	}
	if got[0].Alias != "next-server (v1" {
		t.Errorf("alias from process: %+v", got[0])
	}
	if got[1].Alias != "postgres-1" {
		t.Errorf("alias must prefer the container name: %+v", got[1])
	}
}

func TestBuildRemapsWhenLocalPortIsBusy(t *testing.T) {
	ports := []discover.Port{{Port: 5432, Process: "rootlesskit", Container: "pg"}}
	busy := func(p int) bool { return p != 5432 } // 5432 is taken locally

	got := Build(ports, map[int]bool{}, busy)

	if len(got) != 1 {
		t.Fatalf("got %d assignments", len(got))
	}
	if got[0].LocalPort == 5432 {
		t.Fatal("must not assign a busy local port")
	}
	if !got[0].Remapped {
		t.Error("a remap must be flagged so the caller can report it")
	}
	if got[0].RemotePort != 5432 {
		t.Errorf("remote port must not change: %+v", got[0])
	}
}

func TestBuildAvoidsPortsTakenByOtherEntries(t *testing.T) {
	ports := []discover.Port{{Port: 3100, Process: "app"}}
	got := Build(ports, map[int]bool{3100: true}, allFree)

	if got[0].LocalPort == 3100 {
		t.Fatal("3100 is already used by another entry in the file")
	}
	if !got[0].Remapped {
		t.Error("expected Remapped to be true")
	}
}

func TestBuildDoesNotReuseAPortWithinOneRun(t *testing.T) {
	// Both remote ports want to land on a local port; the second must move.
	ports := []discover.Port{{Port: 4000, Process: "a"}, {Port: 4001, Process: "b"}}
	busy := func(p int) bool { return p != 4001 } // only 4001 is busy locally
	got := Build(ports, map[int]bool{}, busy)

	if got[0].LocalPort == got[1].LocalPort {
		t.Fatalf("two assignments collided: %+v", got)
	}
}

func TestBuildSkipsPortsWithoutLabel(t *testing.T) {
	got := Build([]discover.Port{{Port: 9999}}, map[int]bool{}, allFree)
	if len(got) != 1 {
		t.Fatalf("an unlabelled port is still forwardable: %+v", got)
	}
	if got[0].Alias != "port 9999" {
		t.Errorf("expected a fallback alias, got %q", got[0].Alias)
	}
}
```

- [ ] **Step 2: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./internal/plan/ -v`
Expected: FAIL — `undefined: Build`.

- [ ] **Step 3: Implementar o mínimo**

Criar `internal/plan/plan.go`:

```go
// Package plan turns discovered remote ports into concrete local port
// assignments, resolving collisions against the local machine.
package plan

import (
	"fmt"
	"net"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// Assignment is one remote port paired with the local port to bind it to.
type Assignment struct {
	RemotePort int
	LocalPort  int
	Alias      string
	Remapped   bool // true when LocalPort != RemotePort because of a conflict
}

// LocalPortFree reports whether a TCP port can be bound on this machine.
func LocalPortFree(port int) bool {
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return false
	}
	l.Close()
	return true
}

// Build assigns a local port to every discovered port. The default is the
// remote port itself, which is what the user asked for; when that port is
// already used — by another entry in the config, by another assignment in this
// same run, or by something listening on this machine — the next free port is
// chosen and the assignment is flagged so the caller can say so out loud.
func Build(ports []discover.Port, taken map[int]bool, free func(int) bool) []Assignment {
	used := map[int]bool{}
	for p := range taken {
		used[p] = true
	}

	out := make([]Assignment, 0, len(ports))
	for _, p := range ports {
		a := Assignment{RemotePort: p.Port, LocalPort: p.Port, Alias: alias(p)}
		if used[p.Port] || !free(p.Port) {
			a.LocalPort = nextFree(p.Port, used, free)
			a.Remapped = true
		}
		used[a.LocalPort] = true
		out = append(out, a)
	}
	return out
}

// nextFree walks upward from the wanted port, staying inside the unprivileged
// range and giving up rather than looping forever.
func nextFree(from int, used map[int]bool, free func(int) bool) int {
	for p := from + 1; p < 65536; p++ {
		if p < 1024 {
			continue
		}
		if !used[p] && free(p) {
			return p
		}
	}
	return 0
}

func alias(p discover.Port) string {
	if l := p.Label(); l != "" {
		return l
	}
	return fmt.Sprintf("port %d", p.Port)
}
```

- [ ] **Step 4: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/plan/ -v`
Expected: PASS nos cinco testes.

- [ ] **Step 5: Commit**

```bash
cd ~/projects/portscout
git add internal/plan/
git commit -m "feat(plan): assign local ports and resolve collisions"
```

---

### Task 6: Merge com posse por tag

**Files:**
- Create: `internal/tunnel9/merge.go`
- Test: `internal/tunnel9/merge_test.go`

**Interfaces:**
- Consumes: `Config` da task 4, `plan.Assignment` da task 5.
- Produces:
  - `tunnel9.Change{Kind ChangeKind; Entry Entry}` com `ChangeKind` sendo `Added`, `Updated`, `Pruned`
  - `(*Config).Merge(host string, assignments []plan.Assignment, tag string, prune bool) []Change`
  - `(*Config).TakenLocalPorts() map[int]bool`

  A task 7 consome os três.

- [ ] **Step 1: Escrever o teste que falha**

Criar `internal/tunnel9/merge_test.go`:

```go
package tunnel9

import (
	"os"
	"strings"
	"testing"

	"github.com/MarllonGomes/portscout/internal/plan"
)

func TestMergeAddsNewEntry(t *testing.T) {
	cfg, _ := loadFixture(t)
	changes := cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3100, LocalPort: 3100, Alias: "next-server"},
	}, "auto", false)

	if len(changes) != 1 || changes[0].Kind != Added {
		t.Fatalf("expected one Added change, got %+v", changes)
	}
	var found bool
	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3100 {
			found = true
			if e.LocalPort != 3100 || e.Tag != "auto" || e.Alias != "next-server" {
				t.Errorf("new entry wrong: %+v", e)
			}
		}
	}
	if !found {
		t.Error("new entry was not added")
	}
}

func TestMergePreservesUserEditedLocalPort(t *testing.T) {
	cfg, _ := loadFixture(t)
	// The fixture already has dev:3102 with local_port 4102, edited by the user.
	cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3102, LocalPort: 3102, Alias: "postgres-novo"},
	}, "auto", false)

	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			if e.LocalPort != 4102 {
				t.Errorf("local_port must be preserved, got %d", e.LocalPort)
			}
			if e.Alias != "postgres-novo" {
				t.Errorf("alias should refresh, got %q", e.Alias)
			}
		}
	}
}

func TestMergeNeverTouchesUntaggedEntries(t *testing.T) {
	cfg, path := loadFixture(t)
	cfg.Merge("prod-db.example.com", []plan.Assignment{
		{RemotePort: 5432, LocalPort: 5432, Alias: "hijack"},
	}, "auto", true)

	for _, e := range cfg.Entries() {
		if e.Host == "prod-db.example.com" && e.Tag == "" {
			if e.LocalPort != 15432 || e.Alias != "prod-db" {
				t.Errorf("manual entry was modified: %+v", e)
			}
		}
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(path)
	if !strings.Contains(string(out), "15432") {
		t.Error("manual entry disappeared from the file")
	}
}

func TestMergePruneRemovesOnlyManagedGoneEntries(t *testing.T) {
	cfg, _ := loadFixture(t)
	// dev:3102 is managed and no longer discovered; prune must drop it.
	changes := cfg.Merge("dev", nil, "auto", true)

	var pruned int
	for _, c := range changes {
		if c.Kind == Pruned {
			pruned++
		}
	}
	if pruned != 1 {
		t.Fatalf("expected 1 pruned entry, got %d (%+v)", pruned, changes)
	}
	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			t.Error("managed gone entry should have been pruned")
		}
		if e.Host == "prod-db.example.com" && e.Tag == "" {
			return // manual entry survived, as required
		}
	}
	t.Error("manual entry must survive prune")
}

func TestMergeWithoutPruneKeepsGoneEntries(t *testing.T) {
	cfg, _ := loadFixture(t)
	cfg.Merge("dev", nil, "auto", false)

	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			return
		}
	}
	t.Error("without --prune the entry must stay")
}

func TestTakenLocalPorts(t *testing.T) {
	cfg, _ := loadFixture(t)
	taken := cfg.TakenLocalPorts()
	if !taken[15432] || !taken[4102] {
		t.Errorf("expected both local ports reported as taken, got %+v", taken)
	}
}
```

- [ ] **Step 2: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./internal/tunnel9/ -run TestMerge -v`
Expected: FAIL — `undefined: Merge`, `undefined: Added`.

- [ ] **Step 3: Implementar o mínimo**

Criar `internal/tunnel9/merge.go`:

```go
package tunnel9

import (
	"strconv"

	"github.com/MarllonGomes/portscout/internal/plan"
	"gopkg.in/yaml.v3"
)

// ChangeKind says what happened to one entry during a merge.
type ChangeKind int

const (
	Added ChangeKind = iota
	Updated
	Pruned
)

// Change is a single edit the merge made, for the caller to report.
type Change struct {
	Kind  ChangeKind
	Entry Entry
}

// TakenLocalPorts reports every local port already claimed in the file, so the
// planner does not hand out one that is spoken for.
func (c *Config) TakenLocalPorts() map[int]bool {
	taken := map[int]bool{}
	for _, e := range c.Entries() {
		if e.LocalPort != 0 {
			taken[e.LocalPort] = true
		}
	}
	return taken
}

// Merge reconciles the discovered assignments for one host into the config.
//
// Ownership is the whole point: only entries carrying the managed tag are ever
// written or removed. An entry the user untagged becomes theirs and is skipped,
// and the local_port they edited is never overwritten — the alias is the only
// field a rescan refreshes.
func (c *Config) Merge(host string, assignments []plan.Assignment, tag string, prune bool) []Change {
	var changes []Change

	seen := map[int]bool{}
	for _, a := range assignments {
		seen[a.RemotePort] = true
		if node := c.findManaged(host, a.RemotePort, tag); node != nil {
			if mapGet(node, "alias") != a.Alias {
				mapSet(node, "alias", a.Alias)
				changes = append(changes, Change{Updated, c.entryOf(node)})
			}
			continue
		}
		node := newEntryNode(host, a, tag)
		c.tunnels.Content = append(c.tunnels.Content, node)
		changes = append(changes, Change{Added, c.entryOf(node)})
	}

	if !prune {
		return changes
	}
	kept := c.tunnels.Content[:0]
	for _, node := range c.tunnels.Content {
		if isManaged(node, host, tag) && !seen[mapGetInt(node, "remote_port")] {
			changes = append(changes, Change{Pruned, c.entryOf(node)})
			continue
		}
		kept = append(kept, node)
	}
	c.tunnels.Content = kept
	return changes
}

func (c *Config) findManaged(host string, remotePort int, tag string) *yaml.Node {
	for _, node := range c.tunnels.Content {
		if isManaged(node, host, tag) && mapGetInt(node, "remote_port") == remotePort {
			return node
		}
	}
	return nil
}

func isManaged(node *yaml.Node, host, tag string) bool {
	return node.Kind == yaml.MappingNode &&
		mapGet(node, "host") == host &&
		mapGet(node, "tag") == tag
}

func (c *Config) entryOf(node *yaml.Node) Entry {
	return Entry{
		Host:       mapGet(node, "host"),
		Alias:      mapGet(node, "alias"),
		User:       mapGet(node, "user"),
		LocalPort:  mapGetInt(node, "local_port"),
		RemotePort: mapGetInt(node, "remote_port"),
		Tag:        mapGet(node, "tag"),
	}
}

func newEntryNode(host string, a plan.Assignment, tag string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapSet(node, "host", host)
	mapSet(node, "alias", a.Alias)
	mapSetInt(node, "local_port", a.LocalPort)
	mapSetInt(node, "remote_port", a.RemotePort)
	mapSet(node, "tag", tag)
	return node
}

func mapSet(n *yaml.Node, key, value string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1].Value = value
			n.Content[i+1].Tag = "!!str"
			return
		}
	}
	n.Content = append(n.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func mapSetInt(n *yaml.Node, key string, value int) {
	text := strconv.Itoa(value)
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			n.Content[i+1].Value = text
			n.Content[i+1].Tag = "!!int"
			return
		}
	}
	n.Content = append(n.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: text},
	)
}
```

- [ ] **Step 4: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./internal/tunnel9/ -v`
Expected: PASS em todos os testes do pacote (config + merge).

- [ ] **Step 5: Commit**

```bash
cd ~/projects/portscout
git add internal/tunnel9/
git commit -m "feat(tunnel9): tag-owned merge with prune and preserved local ports"
```

---

### Task 7: CLI — `list`, `scan`, `watch`

**Files:**
- Create: `cmd/portscout/main.go`
- Test: `cmd/portscout/main_test.go`

**Interfaces:**
- Consumes: `discover.Discover`, `discover.SSHRunner`, `discover.Runner`, `plan.Build`, `plan.LocalPortFree`, `tunnel9.Load`, `tunnel9.DefaultPath`, `(*Config).Merge/Save/TakenLocalPorts`, `tunnel9.Change`.
- Produces: o binário. Nada consome esta task.

- [ ] **Step 1: Escrever o teste que falha**

Criar `cmd/portscout/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct{ out string }

func (f fakeRunner) Run(context.Context, string, string) ([]byte, error) {
	return []byte(f.out), nil
}

const fakeRemote = `#portscout:ss
LISTEN 0 511 127.0.0.1:3100 0.0.0.0:* users:(("next-server",pid=1,fd=22))
LISTEN 0 4096 127.0.0.1:3102 0.0.0.0:* users:(("rootlesskit",pid=2,fd=24))
#portscout:docker
{"ID":"a1","Names":"postgres-1","Ports":"127.0.0.1:3102->5432/tcp"}
`

func TestRunListPrintsPortsAndWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"list", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "3100") || !strings.Contains(out.String(), "postgres-1") {
		t.Errorf("expected ports in output:\n%s", out.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("list must not create the config file")
	}
}

func TestRunScanWritesEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	code := run([]string{"scan", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(int) bool { return true },
	})

	if code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(written)
	for _, want := range []string{"remote_port: 3100", "remote_port: 3102", "tag: auto", "postgres-1"} {
		if !strings.Contains(text, want) {
			t.Errorf("config missing %q:\n%s", want, text)
		}
	}
}

func TestRunScanReportsRemap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	var out bytes.Buffer

	run([]string{"scan", "dev"}, options{
		runner:     fakeRunner{out: fakeRemote},
		configPath: path,
		stdout:     &out,
		stderr:     &out,
		portFree:   func(p int) bool { return p != 3100 }, // 3100 busy locally
	})

	if !strings.Contains(out.String(), "remapeada") {
		t.Errorf("a remap must be announced:\n%s", out.String())
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"frobnicate"}, options{stdout: &out, stderr: &out}); code == 0 {
		t.Fatal("unknown command must exit non-zero")
	}
}

func TestRunRequiresHost(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"scan"}, options{stdout: &out, stderr: &out}); code == 0 {
		t.Fatal("missing host must exit non-zero")
	}
}
```

- [ ] **Step 2: Rodar o teste e confirmar que falha**

Run: `~/.local/go/bin/go test ./cmd/portscout/ -v`
Expected: FAIL — `undefined: run`, `undefined: options`.

- [ ] **Step 3: Implementar o mínimo**

Criar `cmd/portscout/main.go`:

```go
// Command portscout discovers listening ports on a remote host and writes them
// into the tunnel9 config. It never opens a tunnel itself.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/MarllonGomes/portscout/internal/discover"
	"github.com/MarllonGomes/portscout/internal/plan"
	"github.com/MarllonGomes/portscout/internal/tunnel9"
)

// options are the seams the tests replace; production fills them in main.
type options struct {
	runner     discover.Runner
	configPath string
	stdout     io.Writer
	stderr     io.Writer
	portFree   func(int) bool
}

func main() {
	os.Exit(run(os.Args[1:], options{}))
}

func run(args []string, o options) int {
	if o.stdout == nil {
		o.stdout = os.Stdout
	}
	if o.stderr == nil {
		o.stderr = os.Stderr
	}
	if len(args) == 0 {
		fmt.Fprintln(o.stderr, usage)
		return 2
	}

	command := args[0]
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	all := fs.Bool("all", false, "não filtrar portas de sistema")
	prune := fs.Bool("prune", false, "remover entradas gerenciadas cuja porta sumiu")
	tag := fs.String("tag", "auto", "tag das entradas gerenciadas")
	configPath := fs.String("config", "", "caminho do YAML do tunnel9")
	every := fs.Duration("every", 15*time.Second, "intervalo do watch")

	switch command {
	case "list", "scan", "watch":
	default:
		fmt.Fprintf(o.stderr, "comando desconhecido: %s\n\n%s\n", command, usage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintf(o.stderr, "falta o host ssh\n\n%s\n", usage)
		return 2
	}
	host := fs.Arg(0)

	if o.runner == nil {
		o.runner = discover.SSHRunner{}
	}
	if o.portFree == nil {
		o.portFree = plan.LocalPortFree
	}
	if o.configPath == "" {
		o.configPath = *configPath
	}
	if o.configPath == "" {
		o.configPath = tunnel9.DefaultPath()
	}

	if command == "watch" {
		for {
			if code := once(o, host, command, *all, *prune, *tag); code != 0 {
				return code
			}
			time.Sleep(*every)
		}
	}
	return once(o, host, command, *all, *prune, *tag)
}

func once(o options, host, command string, all, prune bool, tag string) int {
	ports, err := discover.Discover(context.Background(), o.runner, host, all)
	if err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	if len(ports) == 0 {
		fmt.Fprintf(o.stdout, "nenhuma porta em LISTEN encontrada em %s\n", host)
		return 0
	}

	if command == "list" {
		for _, p := range ports {
			fmt.Fprintf(o.stdout, "  %-6d %s\n", p.Port, p.Label())
		}
		return 0
	}

	cfg, err := tunnel9.Load(o.configPath)
	if err != nil {
		fmt.Fprintf(o.stderr, "%v\naponte outro arquivo com --config\n", err)
		return 1
	}
	assignments := plan.Build(ports, cfg.TakenLocalPorts(), o.portFree)
	for _, a := range assignments {
		if a.Remapped {
			fmt.Fprintf(o.stdout, "  porta %d remapeada para %d (a local estava ocupada)\n",
				a.RemotePort, a.LocalPort)
		}
	}
	changes := cfg.Merge(host, assignments, tag, prune)
	if len(changes) == 0 {
		fmt.Fprintln(o.stdout, "nada mudou")
		return 0
	}
	if err := cfg.Save(o.configPath); err != nil {
		fmt.Fprintln(o.stderr, err)
		return 1
	}
	for _, c := range changes {
		fmt.Fprintf(o.stdout, "  %s %d -> %d  %s\n",
			verb(c.Kind), c.Entry.RemotePort, c.Entry.LocalPort, c.Entry.Alias)
	}
	fmt.Fprintf(o.stdout, "%s atualizado\n", o.configPath)
	return 0
}

func verb(k tunnel9.ChangeKind) string {
	switch k {
	case tunnel9.Added:
		return "+"
	case tunnel9.Pruned:
		return "-"
	default:
		return "~"
	}
}

const usage = `uso:
  portscout list  <ssh-host>    descobre e imprime, sem escrever
  portscout scan  <ssh-host>    descobre e atualiza o YAML do tunnel9
  portscout watch <ssh-host>    scan em loop

flags: --all --prune --tag=auto --config=CAMINHO --every=15s`
```

- [ ] **Step 4: Rodar o teste e confirmar que passa**

Run: `~/.local/go/bin/go test ./... -v`
Expected: PASS em todos os pacotes.

- [ ] **Step 5: Verificar build e vet**

```bash
cd ~/projects/portscout
export PATH="$HOME/.local/go/bin:$PATH"
go vet ./...
go build -o portscout ./cmd/portscout
./portscout
```
Expected: `go vet` silencioso; `./portscout` sem argumentos imprime o uso e sai com código 2.

- [ ] **Step 6: Commit**

```bash
cd ~/projects/portscout
git add cmd/
git commit -m "feat(cmd): list, scan and watch subcommands"
```

---

### Task 8: README, release e publicação privada

**Files:**
- Create: `README.md`
- Create: `.goreleaser.yaml`
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: o binário da task 7.
- Produces: repositório privado publicado.

- [ ] **Step 1: Escrever o README**

Criar `README.md`:

````markdown
# portscout

Descobre portas em LISTEN numa máquina remota e escreve as entradas no YAML do
[tunnel9](https://github.com/sio2boss/tunnel9), que cuida da TUI, do liga/desliga, do
monitoramento e do túnel em si.

`portscout` nunca abre um túnel. Ele só olha o remoto e edita o YAML.

## Uso

```sh
portscout list  dev     # descobre e imprime, sem escrever
portscout scan  dev     # descobre e atualiza o YAML do tunnel9
portscout watch dev     # scan a cada 15s
```

`dev` é qualquer destino que o seu `ssh` entenda — o `~/.ssh/config` é a fonte de máquinas.

Depois de um `scan`, abra o `tunnel9`, filtre pela tag `auto` e aperte `Enter` no que quiser
tunelar.

## O que ele descobre

Une `ss -ltnpH` (processos do host) com `docker ps` (containers), porque com Docker rootless
toda porta publicada aparece no `ss` como `rootlesskit` — o nome utilizável só vem do docker.
Portas de sistema (sshd, resolved, chrony…) ficam de fora; `--all` mostra tudo.

## Regras de convivência com o seu YAML

- Só mexe em entradas com `tag: auto` (mude com `--tag`). O resto do arquivo é intocável.
- O `local_port` que você editar é preservado nas próximas varreduras.
- Porta que sumiu do remoto **não** é apagada, a menos que você passe `--prune`.
- Se a porta local desejada estiver ocupada, escolhe a próxima livre e avisa.
- Escrita atômica; se o YAML não parsear, aborta sem gravar.

## Flags

| Flag | Padrão | Efeito |
| --- | --- | --- |
| `--all` | `false` | Não filtra portas de sistema |
| `--prune` | `false` | Remove entradas gerenciadas cuja porta sumiu |
| `--tag` | `auto` | Tag das entradas gerenciadas |
| `--config` | padrão do tunnel9 | Caminho do YAML |
| `--every` | `15s` | Intervalo do `watch` |

## Instalação

```sh
go install github.com/MarllonGomes/portscout/cmd/portscout@latest
```

Ou baixe o binário da página de releases.
````

- [ ] **Step 2: Escrever a config do goreleaser**

Criar `.goreleaser.yaml`:

```yaml
version: 2
builds:
  - main: ./cmd/portscout
    binary: portscout
    env: [CGO_ENABLED=0]
    goos: [linux, darwin]
    goarch: [amd64, arm64]
    ldflags: ['-s -w']
archives:
  - formats: [tar.gz]
changelog:
  use: github
```

- [ ] **Step 3: Escrever o workflow de release**

Criar `.github/workflows/release.yml`:

```yaml
name: release
on:
  push:
    tags: ['v*']
permissions:
  contents: write
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v5
        with: {fetch-depth: 0}
      - uses: actions/setup-go@v6
        with: {go-version: '1.27'}
      - run: go test ./...
      - uses: goreleaser/goreleaser-action@v6
        with: {args: release --clean}
        env: {GITHUB_TOKEN: '${{ secrets.GITHUB_TOKEN }}'}
```

- [ ] **Step 4: Rodar a suíte completa uma última vez**

```bash
cd ~/projects/portscout
export PATH="$HOME/.local/go/bin:$PATH"
go vet ./... && go test ./... && go build -o portscout ./cmd/portscout
```
Expected: vet silencioso, todos os testes PASS, binário gerado.

- [ ] **Step 5: Commit**

```bash
cd ~/projects/portscout
git add README.md .goreleaser.yaml .github/
git commit -m "docs: readme and release pipeline"
```

- [ ] **Step 6: Publicar como repositório privado**

```bash
cd ~/projects/portscout
gh repo create portscout --private --source=. --remote=origin --push
gh repo view --json name,visibility,url
```
Expected: `visibility` igual a `PRIVATE` e a URL do repo impressa.

---

## Self-Review

**Cobertura do spec:**

| Requisito do spec | Task |
| --- | --- |
| Subcomandos `list`/`scan`/`watch`, host via ssh config | 7 |
| Flags `--all`, `--tag`, `--config`, `--prune` | 7 |
| Um SSH por varredura, snippet POSIX | 3 |
| `ss -ltnpH` | 1 |
| `docker ps` → nome do container | 2 |
| Sem docker no remoto: degrada com aviso | 3 |
| Fallback do socket rootless | 3 |
| Colapso `0.0.0.0`/`::` | 3 |
| Filtro de ruído + `--all` | 3 |
| `yaml.Node` preservando comentários | 4 |
| Posse por `tag: auto` | 6 |
| Entrada nova com `local_port = remote_port` | 5, 6 |
| `local_port` editado preservado | 6 |
| Entrada sem tag intocada | 6 |
| Sumida mantida sem `--prune` | 6 |
| Colisão de porta local + aviso | 5, 7 |
| Escrita atômica + YAML inválido aborta | 4 |
| Runner fake, nenhum teste com SSH | 3, 7 |
| Distribuição privada no GitHub | 8 |

Sem lacunas.

**Placeholders:** nenhum "TBD"/"TODO"; todo passo de código traz o código real.

**Consistência de tipos:** `discover.Port`/`Label()` (task 3) são consumidos por `plan.Build` (5); `plan.Assignment` (5) por `(*Config).Merge` (6); `tunnel9.Change`/`ChangeKind` (6) pela CLI (7). `mapGet`/`mapGetInt` são definidos na task 4 e usados na 6, no mesmo pacote. `markerSS`/`markerDocker` são definidos na task 3 e usados no teste da mesma task.
