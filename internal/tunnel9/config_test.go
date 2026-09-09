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
