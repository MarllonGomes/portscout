package plan

import (
	"testing"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// O mailpit publica duas portas e as duas viram o mesmo nome de container.
// Sem desambiguar, a TUI do tunnel9 mostra duas linhas idênticas.
func TestBuildDisambiguatesRepeatedAliases(t *testing.T) {
	ports := []discover.Port{
		{Port: 3104, Container: "mailpit-1"},
		{Port: 3105, Container: "mailpit-1"},
		{Port: 3106, Container: "redis-1"},
	}
	got := Build(ports, map[int]bool{}, func(int) bool { return true })

	seen := map[string]int{}
	for _, a := range got {
		seen[a.Alias]++
	}
	for alias, n := range seen {
		if n > 1 {
			t.Errorf("alias %q repetiu %d vezes", alias, n)
		}
	}
	if got[0].Alias != "mailpit-1 (3104)" {
		t.Errorf("alias[0] = %q", got[0].Alias)
	}
	if got[1].Alias != "mailpit-1 (3105)" {
		t.Errorf("alias[1] = %q", got[1].Alias)
	}
	// Um nome único não deve ganhar sufixo à toa.
	if got[2].Alias != "redis-1" {
		t.Errorf("alias[2] = %q, nome unico nao devia ganhar sufixo", got[2].Alias)
	}
}

// list e scan têm de mostrar o mesmo nome para a mesma porta.
func TestAliasesMatchesBuild(t *testing.T) {
	ports := []discover.Port{
		{Port: 3104, Container: "mailpit-1"},
		{Port: 3105, Container: "mailpit-1"},
		{Port: 3100, Process: "next-server (v16.3.4)"},
		{Port: 9000},
	}
	names := Aliases(ports)
	for _, a := range Build(ports, map[int]bool{}, func(int) bool { return true }) {
		if names[a.RemotePort] != a.Alias {
			t.Errorf("porta %d: list mostraria %q, scan gravaria %q",
				a.RemotePort, names[a.RemotePort], a.Alias)
		}
	}
	if names[9000] != "port 9000" {
		t.Errorf("porta sem rotulo = %q", names[9000])
	}
}
