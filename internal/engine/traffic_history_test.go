package engine

import (
	"testing"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/subscription"
)

// TestTrafficHistoryPersistsAndPrunes checks that hourly buckets survive a
// restart, stay sorted, and that data older than the retention window is
// dropped on save.
func TestTrafficHistoryPersistsAndPrunes(t *testing.T) {
	dir := t.TempDir()

	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP.Enabled = false
	cfg.Proxy.SOCKS.Enabled = false
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true, Mode: config.ModePriority,
	}}

	store := writeConfig(t, dir, cfg)
	eng := New(store, subscription.NewCache(dir), newTestLogger(t), dir)

	now := time.Now()
	eng.trafficMu.Lock()
	eng.totals = map[string]config.Traffic{"usr_1": {Up: 10, Down: 20}}
	eng.addHistory("usr_1", now.Add(-time.Hour), 5, 6)
	eng.addHistory("usr_1", now, 100, 200)
	eng.addHistory("usr_1", now.Add(-40*24*time.Hour), 7, 8)
	eng.probeTotals = config.Traffic{Up: 1, Down: 2}
	eng.trafficMu.Unlock()

	if err := eng.saveTraffic(); err != nil {
		t.Fatalf("save traffic: %v", err)
	}

	reloaded := New(store, subscription.NewCache(dir), newTestLogger(t), dir)
	if err := reloaded.loadTraffic(); err != nil {
		t.Fatalf("load traffic: %v", err)
	}

	if got := reloaded.Traffic()["usr_1"]; got.Up != 10 || got.Down != 20 {
		t.Fatalf("totals did not survive: %+v", got)
	}
	if got := reloaded.ProbeTraffic(); got.Up != 1 || got.Down != 2 {
		t.Fatalf("probe totals did not survive: %+v", got)
	}

	points := reloaded.TrafficHistory("usr_1", now.Add(-2*time.Hour))
	if len(points) != 2 {
		t.Fatalf("history points = %d, want 2 (the 40-day bucket must be pruned): %+v", len(points), points)
	}
	if !points[0].At.Before(points[1].At) {
		t.Fatalf("history is not sorted oldest first: %+v", points)
	}
	if points[0].Up != 5 || points[0].Down != 6 {
		t.Fatalf("previous-hour bucket = %+v", points[0])
	}
	if points[1].Up != 100 || points[1].Down != 200 {
		t.Fatalf("current-hour bucket = %+v", points[1])
	}
}

// TestResetTrafficKeepsHistory: resetting the running total must not wipe the
// usage chart the operator uses to inspect the last 30 days.
func TestResetTrafficKeepsHistory(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Users = []config.User{{ID: "usr_1", Name: "alice", Password: "pw", Enabled: true, Mode: config.ModePriority}}
	store := writeConfig(t, dir, cfg)
	eng := New(store, subscription.NewCache(dir), newTestLogger(t), dir)

	eng.trafficMu.Lock()
	eng.totals = map[string]config.Traffic{"usr_1": {Up: 10, Down: 20}}
	eng.addHistory("usr_1", time.Now(), 10, 20)
	eng.trafficMu.Unlock()

	eng.ResetTraffic([]string{"usr_1"})
	if got := eng.Traffic()["usr_1"]; got.Up != 0 || got.Down != 0 {
		t.Fatalf("totals were not reset: %+v", got)
	}
	if points := eng.TrafficHistory("usr_1", time.Time{}); len(points) != 1 {
		t.Fatalf("history should survive a reset, got %+v", points)
	}
}
