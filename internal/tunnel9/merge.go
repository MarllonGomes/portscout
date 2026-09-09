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
			// An entry written by an older portscout carries the key names from
			// tunnel9's README instead of the ones its loader actually reads.
			// Rename them in place: the position in the file, the comments and
			// the local_port the user edited all survive.
			migrated := migrateLegacyKeys(node)
			if mapGet(node, keyAlias) != a.Alias || migrated {
				mapSet(node, keyAlias, a.Alias)
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
		entryHost(node) == host &&
		mapGet(node, "tag") == tag
}

// migrateLegacyKeys rewrites the pre-fix key names on one entry and reports
// whether it changed anything.
func migrateLegacyKeys(node *yaml.Node) bool {
	// Both renames must run: || would short-circuit past the second one.
	renamedHost := mapRenameKey(node, legacyKeyHost, keyHost)
	renamedAlias := mapRenameKey(node, legacyKeyAlias, keyAlias)
	return renamedHost || renamedAlias
}

func (c *Config) entryOf(node *yaml.Node) Entry {
	return Entry{
		Host:       entryHost(node),
		Alias:      entryAlias(node),
		LocalPort:  mapGetInt(node, "local_port"),
		RemotePort: mapGetInt(node, "remote_port"),
		Tag:        mapGet(node, "tag"),
	}
}

func newEntryNode(host string, a plan.Assignment, tag string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapSet(node, keyHost, host)
	mapSet(node, keyAlias, a.Alias)
	mapSetInt(node, "local_port", a.LocalPort)
	mapSetInt(node, "remote_port", a.RemotePort)
	mapSet(node, "tag", tag)
	return node
}

// mapRenameKey renames a key, keeping its position and value. It is a no-op if
// the old key is absent or the new one is already there.
func mapRenameKey(n *yaml.Node, old, new string) bool {
	var found *yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch n.Content[i].Value {
		case new:
			return false
		case old:
			found = n.Content[i]
		}
	}
	if found == nil {
		return false
	}
	found.Value = new
	return true
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
