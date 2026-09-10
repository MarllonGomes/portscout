package plan

import (
	"testing"

	"github.com/MarllonGomes/portscout/internal/discover"
)

// O mailpit publica duas portas e as duas viram o mesmo nome de container.
// Sem desambiguar, a lista mostra duas linhas idênticas.
func TestAliasesDisambiguatesRepeatedNames(t *testing.T) {
	ports := []discover.Port{
		{Port: 3104, Container: "mailpit-1"},
		{Port: 3105, Container: "mailpit-1"},
		{Port: 3106, Container: "redis-1"},
	}
	got := Aliases(ports)

	if got[3104] != "mailpit-1 (3104)" {
		t.Errorf("3104 = %q", got[3104])
	}
	if got[3105] != "mailpit-1 (3105)" {
		t.Errorf("3105 = %q", got[3105])
	}
	// Um nome único não deve ganhar sufixo à toa.
	if got[3106] != "redis-1" {
		t.Errorf("3106 = %q, nome unico nao devia ganhar sufixo", got[3106])
	}
}

func TestAliasesPrefersTheContainerName(t *testing.T) {
	// Com docker rootless toda porta publicada reporta o mesmo processo
	// "rootlesskit", então o nome utilizável só vem do container.
	got := Aliases([]discover.Port{
		{Port: 3102, Process: "rootlesskit", Container: "postgres-1"},
		{Port: 3100, Process: "next-server (v16.3.4)"},
	})
	if got[3102] != "postgres-1" {
		t.Errorf("3102 = %q", got[3102])
	}
	if got[3100] != "next-server (v16.3.4)" {
		t.Errorf("3100 = %q", got[3100])
	}
}

func TestAliasesFallsBackToThePortNumber(t *testing.T) {
	got := Aliases([]discover.Port{{Port: 9000}})
	if got[9000] != "port 9000" {
		t.Errorf("porta sem rotulo = %q", got[9000])
	}
}
