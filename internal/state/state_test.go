package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tempStore(t *testing.T) *Store {
	t.Helper()
	return OpenAt(filepath.Join(t.TempDir(), "dev.json"), "dev")
}

func TestSaveLoadRoundTrip(t *testing.T) {
	s := tempStore(t)
	want := Host{Version: Version, Host: "dev", Choices: []Choice{
		{RemotePort: 3100, LocalPort: 3102, Wanted: true, Alias: "next-server", RemoteHost: "127.0.0.1"},
		{RemotePort: 3102, LocalPort: 8080, Wanted: true, Pinned: true, Alias: "postgres-1"},
	}}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}

	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Choices) != 2 {
		t.Fatalf("got %d choices, want 2: %+v", len(got.Choices), got)
	}
	if got.Choices[1].LocalPort != 8080 || !got.Choices[1].Pinned {
		t.Errorf("a pinned port must survive verbatim: %+v", got.Choices[1])
	}
	if got.Host != "dev" {
		t.Errorf("Host = %q", got.Host)
	}
}

// A first run has no file, and that is not a problem worth reporting.
func TestLoadMissingFileIsEmptyAndFine(t *testing.T) {
	got, err := tempStore(t).Load()
	if err != nil {
		t.Fatalf("a missing file must not be an error: %v", err)
	}
	if len(got.Choices) != 0 {
		t.Errorf("expected no choices, got %+v", got.Choices)
	}
}

// Losing checkboxes is annoying; refusing to start is worse. A file we cannot
// understand is discarded with a warning the caller can show.
func TestLoadDiscardsAnUnreadableFile(t *testing.T) {
	for _, body := range []string{`{"version":99,"host":"dev"}`, `not json at all`, ``} {
		path := filepath.Join(t.TempDir(), "dev.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		got, err := OpenAt(path, "dev").Load()
		if !errors.Is(err, ErrDiscarded) {
			t.Errorf("body %q: err = %v, want ErrDiscarded", body, err)
		}
		if len(got.Choices) != 0 {
			t.Errorf("body %q: expected an empty host, got %+v", body, got)
		}
	}
}

// Only choices worth remembering are written, so the file self-cleans when the
// user turns something off and never needs a prune.
func TestSaveKeepsOnlyWantedOrPinnedChoices(t *testing.T) {
	s := tempStore(t)
	err := s.Save(Host{Host: "dev", Choices: []Choice{
		{RemotePort: 3100, LocalPort: 3100, Wanted: true},
		{RemotePort: 3101, LocalPort: 3101},               // untouched: derivable from a scan
		{RemotePort: 3102, LocalPort: 9999, Pinned: true}, // port the user typed
	}})
	if err != nil {
		t.Fatal(err)
	}

	got, _ := s.Load()
	if len(got.Choices) != 2 {
		t.Fatalf("got %d choices, want 2: %+v", len(got.Choices), got.Choices)
	}
	for _, c := range got.Choices {
		if c.RemotePort == 3101 {
			t.Error("an untouched row must not be persisted")
		}
	}
}

func TestSaveSortsChoicesForACleanDiff(t *testing.T) {
	s := tempStore(t)
	_ = s.Save(Host{Host: "dev", Choices: []Choice{
		{RemotePort: 3105, Wanted: true},
		{RemotePort: 3100, Wanted: true},
		{RemotePort: 3102, Wanted: true},
	}})

	got, _ := s.Load()
	for i := 1; i < len(got.Choices); i++ {
		if got.Choices[i-1].RemotePort > got.Choices[i].RemotePort {
			t.Fatalf("choices are not sorted: %+v", got.Choices)
		}
	}
}

// A rescan refreshes aliases every few seconds. Rewriting an identical file
// thousands of times a day is pure wear for no gain.
func TestSaveSkipsAnIdenticalWrite(t *testing.T) {
	s := tempStore(t)
	h := Host{Host: "dev", Choices: []Choice{{RemotePort: 3100, LocalPort: 3100, Wanted: true}}}
	if err := s.Save(h); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Save(h); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !first.ModTime().Equal(second.ModTime()) {
		t.Error("an unchanged save must not touch the file")
	}
}

func TestSaveIsAtomicAndPrivate(t *testing.T) {
	s := tempStore(t)
	if err := s.Save(Host{Host: "dev", Choices: []Choice{{RemotePort: 1, Wanted: true}}}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode = %o, want 600", perm)
	}
	// A crash mid-write must never leave a temp file behind as the real thing.
	entries, _ := os.ReadDir(filepath.Dir(s.Path()))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".portscout-") {
			t.Errorf("a temp file survived the write: %s", e.Name())
		}
	}
}

func TestSaveReportsAnUnwritableLocation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ro")
	if err := os.Mkdir(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	s := OpenAt(filepath.Join(dir, "sub", "dev.json"), "dev")
	if err := s.Save(Host{Host: "dev", Choices: []Choice{{RemotePort: 1, Wanted: true}}}); err == nil {
		t.Error("expected an error writing into a read-only directory")
	}
}

// Two dashboards on two hosts must not share a file: they would race
// read-modify-write and silently drop each other's choices.
func TestPathIsPerHostAndFilesystemSafe(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	dev, err := Open("dev")
	if err != nil {
		t.Fatal(err)
	}
	other, err := Open("user@10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if dev.Path() == other.Path() {
		t.Error("different hosts must not share a state file")
	}
	if strings.ContainsAny(filepath.Base(other.Path()), "@/:") {
		t.Errorf("unsafe file name: %q", other.Path())
	}
}
