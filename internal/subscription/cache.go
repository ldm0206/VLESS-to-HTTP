package subscription

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ldm0206/vless-to-http/internal/node"
)

type cacheEntry struct {
	Updated time.Time   `json:"updated"`
	Format  string      `json:"format"`
	Nodes   []node.Node `json:"nodes"`
}

type cacheFile struct {
	Version int                   `json:"version"`
	Subs    map[string]cacheEntry `json:"subs"`
}

// Cache keeps the parsed nodes of every subscription on disk so a restart does
// not depend on the provider being reachable.
type Cache struct {
	mu   sync.RWMutex
	path string
	subs map[string]cacheEntry
}

// NewCache points a cache at <dataDir>/cache/nodes.json.
func NewCache(dataDir string) *Cache {
	return &Cache{
		path: filepath.Join(dataDir, "cache", "nodes.json"),
		subs: map[string]cacheEntry{},
	}
}

// Load reads the cache file if it exists.
func (c *Cache) Load() error {
	raw, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file cacheFile
	if err := json.Unmarshal(raw, &file); err != nil {
		// A corrupt cache is not fatal: subscriptions refill it.
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if file.Subs != nil {
		c.subs = file.Subs
	}
	return nil
}

// Nodes returns a copy of the cached nodes for one subscription.
func (c *Cache) Nodes(subID string) []node.Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.subs[subID]
	if !ok {
		return nil
	}
	out := make([]node.Node, len(entry.Nodes))
	copy(out, entry.Nodes)
	return out
}

// Updated reports when a subscription was last parsed successfully.
func (c *Cache) Updated(subID string) time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.subs[subID].Updated
}

// Set stores nodes for a subscription and persists the cache.
func (c *Cache) Set(subID string, nodes []node.Node, format string) error {
	c.mu.Lock()
	c.subs[subID] = cacheEntry{Updated: time.Now(), Format: format, Nodes: nodes}
	c.mu.Unlock()
	return c.save()
}

// Delete drops a subscription's cached nodes.
func (c *Cache) Delete(subID string) error {
	c.mu.Lock()
	delete(c.subs, subID)
	c.mu.Unlock()
	return c.save()
}

// All returns every cached node grouped by subscription id.
func (c *Cache) All() map[string][]node.Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string][]node.Node, len(c.subs))
	for id, entry := range c.subs {
		nodes := make([]node.Node, len(entry.Nodes))
		copy(nodes, entry.Nodes)
		out[id] = nodes
	}
	return out
}

// Find resolves a node inside a subscription by id, then by exact name, then
// by name without surrounding whitespace/case differences.
func (c *Cache) Find(subID, key string) *node.Node {
	c.mu.RLock()
	defer c.mu.RUnlock()
	nodes := c.subs[subID].Nodes
	for i := range nodes {
		if nodes[i].ID == key {
			return &nodes[i]
		}
	}
	for i := range nodes {
		if nodes[i].Name == key {
			return &nodes[i]
		}
	}
	for i := range nodes {
		if equalFoldTrim(nodes[i].Name, key) {
			return &nodes[i]
		}
	}
	return nil
}

func equalFoldTrim(a, b string) bool { return strings.EqualFold(a, b) }

func (c *Cache) save() error {
	c.mu.RLock()
	file := cacheFile{Version: 1, Subs: c.subs}
	c.mu.RUnlock()

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
