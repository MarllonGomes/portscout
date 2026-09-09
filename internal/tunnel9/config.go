// Package tunnel9 reads and writes the YAML config of the tunnel9 TUI.
package tunnel9

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

// The key names tunnel9's loader actually reads. Its README documents "host"
// and "alias", but the code has used these since its first commit; an entry
// written with the README's names loads with an empty host, so tunnel9 has
// nowhere to connect and shows no name in the TUI.
const (
	keyHost  = "remote_host"
	keyAlias = "name"

	legacyKeyHost  = "host"
	legacyKeyAlias = "alias"
)

// entryHost reads the SSH destination, accepting the legacy key so a file
// written by an older portscout still round-trips.
func entryHost(n *yaml.Node) string {
	if v := mapGet(n, keyHost); v != "" {
		return v
	}
	return mapGet(n, legacyKeyHost)
}

func entryAlias(n *yaml.Node) string {
	if v := mapGet(n, keyAlias); v != "" {
		return v
	}
	return mapGet(n, legacyKeyAlias)
}

// Entry is one tunnel9 tunnel.
type Entry struct {
	Host       string
	Alias      string
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
			Host:       entryHost(n),
			Alias:      entryAlias(n),
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
	// yaml.Marshal defaults to a four-space indent, which would reformat every
	// line of a file the user also edits by hand. Match tunnel9's two spaces so
	// a scan shows up as the entries it changed and nothing else.
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(c.doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	out := buf.Bytes()
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
