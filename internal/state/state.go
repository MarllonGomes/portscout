// Package state remembers, per host, which remote ports the user wants
// forwarded and where they want them locally.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Version is the on-disk format. A file from the future is discarded rather than
// guessed at.
const Version = 1

// ErrDiscarded says the previous state could not be used and was ignored. It is
// never fatal: losing checkboxes is annoying, refusing to start is worse.
var ErrDiscarded = errors.New("estado anterior ignorado")

// Choice is one remembered decision about a remote port.
type Choice struct {
	RemotePort int `json:"remote_port"`
	// LocalPort is always written, even when it equals the remote port: an
	// auto-started tunnel has to land where it landed last time, or the user's
	// bookmarks and database clients break.
	LocalPort  int       `json:"local_port"`
	Pinned     bool      `json:"pinned,omitempty"` // typed by the user; never remapped
	Wanted     bool      `json:"wanted,omitempty"`
	Alias      string    `json:"alias,omitempty"` // last seen name, so rows render before the first scan
	RemoteHost string    `json:"remote_host,omitempty"`
	LastSeen   time.Time `json:"last_seen,omitempty"`
}

// worthKeeping reports whether a choice carries any user decision. A row nobody
// touched is fully derivable from the next scan, so it never reaches the file —
// which is also why there is no prune to write.
func (c Choice) worthKeeping() bool { return c.Wanted || c.Pinned }

// Host is the whole file.
type Host struct {
	Version int      `json:"version"`
	Host    string   `json:"host"`
	Choices []Choice `json:"choices"`
}

// Store reads and writes one host's file.
//
// A Store is owned by one goroutine. It caches the bytes of its last write to
// skip redundant saves, so sharing one across goroutines races on that cache.
// Open a second Store on the same path to read it from elsewhere.
type Store struct {
	path string
	host string
	last []byte // the bytes of the last successful write
}

// Open resolves the standard location for a host. It creates nothing.
func Open(host string) (*Store, error) {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("localizando o diretório de estado: %w", err)
		}
		dir = filepath.Join(home, ".local", "state")
	}
	// One file per host, never one file with a map of hosts: two dashboards in
	// two terminals would otherwise race read-modify-write and each would
	// silently drop the other's choices.
	return OpenAt(filepath.Join(dir, "portscout", "hosts", safeName(host)+".json"), host), nil
}

// safeName percent-encodes anything outside a deliberately small safe set.
//
// An ssh destination can be "user@host", an IPv6 literal or carry a path
// separator, none of which belong in a file name. url.PathEscape is not enough:
// "@" is legal in a path segment, so it survives unescaped.
func safeName(host string) string {
	var b strings.Builder
	for _, r := range host {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			fmt.Fprintf(&b, "%%%02X", r)
		}
	}
	return b.String()
}

// OpenAt points a store at an explicit path. Tests use it to stay in a temp dir.
func OpenAt(path, host string) *Store { return &Store{path: path, host: host} }

func (s *Store) Path() string { return s.path }

// Load reads the remembered choices. A missing file is not an error; an
// unreadable one yields an empty host wrapped in ErrDiscarded.
func (s *Store) Load() (Host, error) {
	raw, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return Host{Version: Version, Host: s.host}, nil
	}
	if err != nil {
		return Host{Version: Version, Host: s.host}, fmt.Errorf("%w: %v", ErrDiscarded, err)
	}

	var h Host
	if err := json.Unmarshal(raw, &h); err != nil {
		return Host{Version: Version, Host: s.host}, fmt.Errorf("%w: %s não é JSON válido", ErrDiscarded, s.path)
	}
	if h.Version != Version {
		return Host{Version: Version, Host: s.host},
			fmt.Errorf("%w: formato %d, esperado %d", ErrDiscarded, h.Version, Version)
	}
	s.last = raw
	return h, nil
}

// Save writes the choices worth keeping, atomically.
func (s *Store) Save(h Host) error {
	h.Version = Version
	if h.Host == "" {
		h.Host = s.host
	}

	kept := make([]Choice, 0, len(h.Choices))
	for _, c := range h.Choices {
		if c.worthKeeping() {
			kept = append(kept, c)
		}
	}
	// Sorted so the file diffs cleanly and a human reading it finds a port where
	// they expect it.
	sort.Slice(kept, func(i, j int) bool { return kept[i].RemotePort < kept[j].RemotePort })
	h.Choices = kept

	out, err := json.MarshalIndent(h, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')

	// A rescan refreshes aliases every few seconds; rewriting an identical file
	// thousands of times a day buys nothing.
	if bytes.Equal(out, s.last) {
		return nil
	}
	if err := s.writeAtomic(out); err != nil {
		return err
	}
	s.last = out
	return nil
}

// writeAtomic renames a fully written temp file into place. A crash mid-write
// must never leave the user with a truncated list of choices, and rename is the
// only atomic primitive the filesystem gives us.
func (s *Store) writeAtomic(out []byte) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("criando %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".portscout-*")
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
	return os.Rename(tmp.Name(), s.path)
}
