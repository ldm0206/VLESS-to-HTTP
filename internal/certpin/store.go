package certpin

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// TTL is how long a successful inspection is trusted before it is redone, so a
// renewed certificate is picked up without a restart.
const TTL = time.Hour

// FailedTTL is how long a failed inspection is remembered. It is short because
// the usual cause is a node that is briefly unreachable.
const FailedTTL = 2 * time.Minute

// Entry is what one inspection of a node is remembered as.
type Entry struct {
	// Verified is true when the certificate passed normal verification, so the
	// node carries no pin.
	Verified bool `json:"verified,omitempty"`
	// Sha256 is the pinned leaf certificate, empty when the node verifies
	// against the system roots.
	Sha256 string `json:"sha256,omitempty"`
	// Checked is when the inspection ran; it decides when it is redone.
	Checked time.Time `json:"checked"`
	// LastError is why the last inspection failed, for diagnostics.
	LastError string `json:"last_error,omitempty"`
}

// Fresh reports whether the entry is recent enough to be used as is.
func (e Entry) Fresh(now time.Time) bool {
	if e.Checked.IsZero() {
		return false
	}
	ttl := TTL
	if e.LastError != "" {
		ttl = FailedTTL
	}
	return now.Sub(e.Checked) < ttl
}

type storeFile struct {
	Version int              `json:"version"`
	Nodes   map[string]Entry `json:"nodes"`
}

// Store keeps the certificate decision of every node on disk, so a restart
// does not have to inspect them again.
type Store struct {
	mu      sync.RWMutex
	path    string
	entries map[string]Entry
}

// NewStore points a store at <dataDir>/state/certs.json.
func NewStore(dataDir string) *Store {
	return &Store{
		path:    filepath.Join(dataDir, "state", "certs.json"),
		entries: map[string]Entry{},
	}
}

// Load reads the store if it exists.
func (s *Store) Load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file storeFile
	if err := json.Unmarshal(raw, &file); err != nil {
		// A corrupt store is not fatal: every node is inspected again.
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if file.Nodes != nil {
		s.entries = file.Nodes
	}
	return nil
}

// Get returns what is remembered about one node.
func (s *Store) Get(nodeID string) Entry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.entries[nodeID]
}

// Put remembers an inspection and persists the store.
func (s *Store) Put(nodeID string, entry Entry) error {
	s.mu.Lock()
	s.entries[nodeID] = entry
	s.mu.Unlock()
	return s.save()
}

// Forget drops entries for nodes that no longer exist, so the store cannot
// grow across subscription updates.
func (s *Store) Forget(present map[string]bool) error {
	s.mu.Lock()
	changed := false
	for id := range s.entries {
		if !present[id] {
			delete(s.entries, id)
			changed = true
		}
	}
	s.mu.Unlock()
	if !changed {
		return nil
	}
	return s.save()
}

func (s *Store) save() error {
	s.mu.RLock()
	file := storeFile{Version: 1, Nodes: s.entries}
	s.mu.RUnlock()

	raw, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
