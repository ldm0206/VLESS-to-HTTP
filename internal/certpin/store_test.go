package certpin

import (
	"os"
	"testing"
	"time"
)

func TestStoreSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	now := time.Now()
	if err := store.Put("node_a", Entry{Sha256: "ab", Checked: now}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Put("node_b", Entry{Verified: true, Checked: now}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if _, err := os.Stat(store.path); err != nil {
		t.Fatalf("the store was not written to disk: %v", err)
	}

	reopened := NewStore(dir)
	if err := reopened.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := reopened.Get("node_a"); got.Sha256 != "ab" {
		t.Fatalf("node_a = %+v", got)
	}
	if got := reopened.Get("node_b"); !got.Verified || got.Sha256 != "" {
		t.Fatalf("node_b = %+v", got)
	}
}

func TestForgetDropsGoneNodes(t *testing.T) {
	store := NewStore(t.TempDir())
	if err := store.Put("node_a", Entry{Sha256: "ab", Checked: time.Now()}); err != nil {
		t.Fatalf("put: %v", err)
	}
	if err := store.Put("node_b", Entry{Sha256: "cd", Checked: time.Now()}); err != nil {
		t.Fatalf("put: %v", err)
	}

	if err := store.Forget(map[string]bool{"node_a": true}); err != nil {
		t.Fatalf("forget: %v", err)
	}
	if got := store.Get("node_a"); got.Sha256 != "ab" {
		t.Fatalf("a node that still exists was dropped: %+v", got)
	}
	if got := store.Get("node_b"); got.Sha256 != "" {
		t.Fatalf("a node that disappeared was kept: %+v", got)
	}
}

func TestFreshWindow(t *testing.T) {
	now := time.Now()
	if (Entry{}).Fresh(now) {
		t.Fatal("a node that was never inspected is not fresh")
	}
	if !(Entry{Sha256: "ab", Checked: now.Add(-30 * time.Minute)}).Fresh(now) {
		t.Fatal("a successful inspection is trusted for TTL")
	}
	if (Entry{Sha256: "ab", Checked: now.Add(-2 * TTL)}).Fresh(now) {
		t.Fatal("an inspection older than TTL should be redone")
	}
	// A failure is retried much sooner, so a node that was briefly unreachable
	// does not stay unpinned for a whole TTL.
	failed := Entry{Checked: now.Add(-FailedTTL - time.Minute), LastError: "连接超时"}
	if failed.Fresh(now) {
		t.Fatal("a failed inspection should be retried after FailedTTL")
	}
	if !(Entry{Checked: now.Add(-time.Minute), LastError: "连接超时"}).Fresh(now) {
		t.Fatal("a failed inspection should not be retried immediately")
	}
}
