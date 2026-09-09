package tunnel9

import (
	"os"
	"strings"
	"testing"

	"github.com/MarllonGomes/portscout/internal/plan"
)

// tunnel9 lê "remote_host" e "name" — nunca "host"/"alias", apesar do que o
// README dele diz. Uma entrada com as chaves erradas carrega com host vazio e
// o túnel não tem para onde conectar.
func TestNewEntryUsesTunnel9Keys(t *testing.T) {
	cfg, path := loadFixture(t)
	cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3100, LocalPort: 3100, Alias: "next-server"},
	}, "auto", false)
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, "remote_host: dev") {
		t.Errorf("entrada nova deve usar remote_host, YAML gravado:\n%s", got)
	}
	if !strings.Contains(got, "name: next-server") {
		t.Errorf("entrada nova deve usar name, YAML gravado:\n%s", got)
	}
}

// Quem já rodou o portscout tem um arquivo cheio de entradas com as chaves
// antigas. Um rescan deve consertá-las no lugar, preservando o local_port que
// o usuário editou, e não duplicar a porta.
func TestMergeMigratesLegacyKeys(t *testing.T) {
	cfg, path := loadFixture(t)
	changes := cfg.Merge("dev", []plan.Assignment{
		{RemotePort: 3102, LocalPort: 3102, Alias: "postgres"},
	}, "auto", false)

	var n int
	for _, e := range cfg.Entries() {
		if e.Host == "dev" && e.RemotePort == 3102 {
			n++
			if e.LocalPort != 4102 {
				t.Errorf("local_port editado deve sobreviver à migração, got %d", e.LocalPort)
			}
			if e.Alias != "postgres" {
				t.Errorf("alias deve atualizar, got %q", e.Alias)
			}
		}
	}
	if n != 1 {
		t.Errorf("esperava 1 entrada para remote_port 3102, achei %d (duplicou?) changes=%+v", n, changes)
	}
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(path)
	// Âncora no início da linha: "host: " casa como substring de "remote_host: ".
	if strings.Contains(string(raw), "- host: \"dev\"") || strings.Contains(string(raw), "alias: \"postgres") {
		t.Errorf("chaves antigas devem ter sido reescritas, YAML:\n%s", raw)
	}
	// A entrada sem tag não é do portscout e não pode ser migrada.
	if !strings.Contains(string(raw), "- host: \"prod-db.example.com\"") {
		t.Errorf("entrada sem tag deve ficar intocada, YAML:\n%s", raw)
	}
}
