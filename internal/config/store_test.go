package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	return store
}

func TestFirstStartCreatesConfigAndPassword(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	boot, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !boot.Created {
		t.Fatal("the first load should report that it created the file")
	}
	if boot.AdminPassword == "" {
		t.Fatal("a random admin password should have been generated")
	}
	if cfg := store.Get(); cfg.Panel.Admin.PasswordHash == "" || cfg.Panel.APIToken == "" {
		t.Fatalf("bootstrap did not fill in credentials: %+v", cfg.Panel)
	}

	if runtime.GOOS != "windows" {
		// Windows does not carry Unix permission bits; the container does.
		info, err := os.Stat(store.Path())
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("config permissions = %o, want 600 (it holds plaintext passwords)", perm)
		}
	}

	// A second start must not regenerate anything.
	again := NewStore(dir)
	boot2, err := again.Load()
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if boot2.Created || boot2.AdminPassword != "" {
		t.Fatalf("reload regenerated credentials: %+v", boot2)
	}
}

func TestAdminPasswordFromEnvironment(t *testing.T) {
	t.Setenv("V2H_ADMIN_PASSWORD", "from-the-environment")
	dir := t.TempDir()
	store := NewStore(dir)
	boot, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if boot.AdminPassword != "" {
		t.Fatal("no password should be printed when it came from the environment")
	}
	if !strings.Contains(store.Get().Panel.Admin.PasswordHash, "$2") {
		t.Fatal("the environment password was not hashed into the config")
	}
}

func TestUpdateIncrementsRevisionAndNotifies(t *testing.T) {
	store := newTestStore(t)
	before := store.Get().Revision

	var notified int
	store.OnChange(func(*Config) { notified++ })

	if err := store.Update(func(c *Config) error {
		c.Proxy.Fallback = FallbackDirect
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := store.Get().Revision; got != before+1 {
		t.Fatalf("revision = %d, want %d", got, before+1)
	}
	if notified != 1 {
		t.Fatalf("watchers notified %d times", notified)
	}
	if store.Get().Proxy.Fallback != FallbackDirect {
		t.Fatal("the change was not applied")
	}
}

func TestInvalidUpdateIsRejectedAndNotWritten(t *testing.T) {
	store := newTestStore(t)
	original := store.Get().Proxy.Fallback

	err := store.Update(func(c *Config) error {
		c.Proxy.Fallback = "sideways"
		return nil
	})
	if err == nil {
		t.Fatal("an illegal fallback should be rejected")
	}
	if store.Get().Proxy.Fallback != original {
		t.Fatal("the in-memory state was changed by a rejected update")
	}
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(string(raw), "sideways") {
		t.Fatal("a rejected update reached the file")
	}
}

func TestUpdateFailureLeavesStateUntouched(t *testing.T) {
	store := newTestStore(t)
	before := store.Get().Revision

	err := store.Update(func(c *Config) error {
		c.Users = append(c.Users, User{Name: "broken"})
		return errTest
	})
	if err == nil {
		t.Fatal("expected the callback error to surface")
	}
	if store.Get().Revision != before || len(store.Get().Users) != 0 {
		t.Fatal("state changed even though the callback failed")
	}
}

func TestValidationCatchesCommonMistakes(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"duplicate user", func(c *Config) {
			c.Users = []User{{Name: "a", Password: "x"}, {Name: "a", Password: "y"}}
		}, "重复"},
		{"bad mode", func(c *Config) {
			c.Users = []User{{Name: "a", Password: "x", Mode: "teleport"}}
		}, "模式非法"},
		{"bad fallback", func(c *Config) {
			c.Users = []User{{Name: "a", Password: "x", Fallback: "maybe"}}
		}, "兜底策略非法"},
		{"enabled without password", func(c *Config) {
			c.Users = []User{{Name: "a", Enabled: true}}
		}, "没有设置密码"},
		{"username with a colon", func(c *Config) {
			c.Users = []User{{Name: "a:b", Password: "x"}}
		}, "空格或冒号"},
		{"unknown subscription", func(c *Config) {
			c.Users = []User{{Name: "a", Password: "x", Targets: []Target{{Sub: "sub_missing", Node: "n"}}}}
		}, "不存在的订阅"},
		{"target without node", func(c *Config) {
			c.Users = []User{{Name: "a", Password: "x", Targets: []Target{{Sub: ""}}}}
		}, "既没有指定节点"},
		{"duplicate subscription name", func(c *Config) {
			c.Subscriptions = []Subscription{{Name: "机场"}, {Name: "机场"}}
		}, "订阅名称重复"},
		{"bad subscription kind", func(c *Config) {
			c.Subscriptions = []Subscription{{Name: "机场", Kind: "magic"}}
		}, "类型非法"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			tc.mutate(cfg)
			err := cfg.Validate()
			if err == nil {
				t.Fatalf("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.want)
			}
		})
	}
}

// Subscriptions may be referenced by name in a hand-written file; the store
// normalises them to ids so everything downstream can rely on that.
func TestTargetsAcceptSubscriptionNames(t *testing.T) {
	cfg := Default()
	cfg.Subscriptions = []Subscription{{ID: "sub_1", Name: "机场A", Kind: "auto"}}
	cfg.Users = []User{{
		Name: "alice", Password: "pw",
		Targets: []Target{{Sub: "机场A", Node: "香港01"}},
	}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if got := cfg.Users[0].Targets[0].Sub; got != "sub_1" {
		t.Fatalf("target sub = %q, want the id", got)
	}
}

func TestIDsAndDefaults(t *testing.T) {
	cfg := Default()
	cfg.Subscriptions = []Subscription{{Name: "机场A"}}
	cfg.Users = []User{{Name: "alice", Password: "pw"}}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.HasPrefix(cfg.Subscriptions[0].ID, "sub_") {
		t.Fatalf("subscription id = %q", cfg.Subscriptions[0].ID)
	}
	if !strings.HasPrefix(cfg.Users[0].ID, "usr_") {
		t.Fatalf("user id = %q", cfg.Users[0].ID)
	}
	if cfg.Users[0].Mode != ModePriority || cfg.Users[0].Fallback != FallbackInherit {
		t.Fatalf("defaults were not applied: %+v", cfg.Users[0])
	}
	if cfg.Subscriptions[0].Kind != "auto" {
		t.Fatalf("subscription kind = %q", cfg.Subscriptions[0].Kind)
	}
	if cfg.Subscriptions[0].Interval.D() != 12*time.Hour {
		t.Fatalf("subscription interval = %v", cfg.Subscriptions[0].Interval)
	}
}

func TestExternalEditIsPickedUp(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	_ = store.Update(func(c *Config) error { return nil })

	// Simulate another process editing the file: bump the revision and change
	// a value, exactly as a hand edit plus a panel save would.
	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := strings.Replace(string(raw), "fallback: reject", "fallback: direct", 1)
	edited = strings.Replace(edited, "revision: 1", "revision: 99", 1)
	if edited == string(raw) {
		t.Fatalf("test setup did not change the file:\n%s", raw)
	}
	if err := os.WriteFile(store.Path(), []byte(edited), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Give the file an mtime that cannot collide with the one the store
	// recorded for its own last write. On Windows the system clock that
	// time.Now() reads and the one that stamps file writes advance in the
	// same coarse steps (~0.5ms), so a rewrite immediately after that write
	// can carry a bit-identical mtime and look unchanged.
	past := time.Now().Add(-time.Hour)
	if err := os.Chtimes(store.Path(), past, past); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	done := make(chan *Config, 1)
	store.Watch(20*time.Millisecond, func(c *Config) { done <- c }, func(err error) {
		t.Logf("watcher error: %v", err)
	})

	select {
	case cfg := <-done:
		if cfg.Proxy.Fallback != FallbackDirect {
			t.Fatalf("reloaded fallback = %q", cfg.Proxy.Fallback)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the watcher never noticed the edit")
	}
}

func TestDurationParsing(t *testing.T) {
	cfg := Default()
	cfg.Health.Interval = Duration(90 * time.Second)
	root := t.TempDir()
	store := NewStore(root)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := store.Update(func(c *Config) error {
		*c = *cfg
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	reloaded := NewStore(root)
	if _, err := reloaded.Load(); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if got := reloaded.Get().Health.Interval.D(); got != 90*time.Second {
		t.Fatalf("interval = %v, want 1m30s", got)
	}
	if got := reloaded.Get().Proxy.Timeout.D(); got != 300*time.Second {
		t.Fatalf("timeout = %v", got)
	}
}

func TestCloneIsDeep(t *testing.T) {
	cfg := Default()
	cfg.Subscriptions = []Subscription{{ID: "sub_1", Name: "机场A", Kind: "auto"}}
	cfg.Users = []User{{
		ID: "usr_1", Name: "alice", Password: "pw",
		Targets: []Target{{Sub: "sub_1", Node: "香港01"}},
	}}

	clone := cfg.Clone()
	clone.Users[0].Targets[0].Node = "别的节点"
	clone.Subscriptions[0].Name = "改了"

	if cfg.Users[0].Targets[0].Node != "香港01" {
		t.Fatal("clone shares the targets slice with the original")
	}
	if cfg.Subscriptions[0].Name != "机场A" {
		t.Fatal("clone shares the subscriptions slice with the original")
	}
}

func TestEffectiveFallback(t *testing.T) {
	cfg := Default()
	cfg.Proxy.Fallback = FallbackDirect

	inherit := &User{Fallback: FallbackInherit}
	if got := cfg.EffectiveFallback(inherit); got != FallbackDirect {
		t.Fatalf("inherit = %q", got)
	}
	reject := &User{Fallback: FallbackReject}
	if got := cfg.EffectiveFallback(reject); got != FallbackReject {
		t.Fatalf("explicit = %q", got)
	}
}

var errTest = testError("boom")

type testError string

func (e testError) Error() string { return string(e) }

func TestConfigPathIsInDataDir(t *testing.T) {
	dir := t.TempDir()
	store := NewStore(dir)
	if store.Path() != filepath.Join(dir, "config.yaml") {
		t.Fatalf("path = %q", store.Path())
	}
	if store.DataDir() != dir {
		t.Fatalf("data dir = %q", store.DataDir())
	}
}
