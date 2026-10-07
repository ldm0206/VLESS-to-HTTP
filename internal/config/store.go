package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ldm0206/vless-to-http/internal/auth"
)

// Store owns config.yaml. Readers get an immutable snapshot; writers go
// through Update, which clones, mutates, validates, saves atomically and then
// swaps the snapshot in one step.
type Store struct {
	mu       sync.RWMutex
	path     string
	dataDir  string
	cfg      *Config
	watchers []func(*Config)
	modTime  time.Time
}

// Bootstrap reports what happened while loading, so a first start can print
// the generated credentials once.
type Bootstrap struct {
	Created       bool
	AdminPassword string
}

// NewStore returns a store rooted at dataDir; nothing is read until Load.
func NewStore(dataDir string) *Store {
	return &Store{
		dataDir: dataDir,
		path:    filepath.Join(dataDir, "config.yaml"),
	}
}

// Path is the config file location.
func (s *Store) Path() string { return s.path }

// DataDir is the directory holding config, cache and logs.
func (s *Store) DataDir() string { return s.dataDir }

// Get returns the current snapshot. Callers must not mutate it.
func (s *Store) Get() *Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cfg
}

// Load reads config.yaml, creating a default one on first start.
func (s *Store) Load() (*Bootstrap, error) {
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	boot := &Bootstrap{}
	cfg := Default()

	raw, err := os.ReadFile(s.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		boot.Created = true
		boot.AdminPassword = bootstrapAdmin(cfg)
	default:
		if err != nil {
			return nil, fmt.Errorf("read config: %w", err)
		}
		if err := yaml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", s.path, err)
		}
		applyDefaults(cfg)
		if cfg.Panel.Admin.PasswordHash == "" {
			boot.AdminPassword = bootstrapAdmin(cfg)
		}
	}

	if cfg.Panel.APIToken == "" {
		cfg.Panel.APIToken = NewToken()
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.cfg = cfg
	s.mu.Unlock()

	if boot.Created {
		if err := s.Save(); err != nil {
			return nil, err
		}
	}
	if st, err := os.Stat(s.path); err == nil {
		s.modTime = st.ModTime()
	}
	return boot, nil
}

// OnChange registers a callback fired after every committed change.
func (s *Store) OnChange(fn func(*Config)) {
	s.mu.Lock()
	s.watchers = append(s.watchers, fn)
	s.mu.Unlock()
}

// Update applies fn to a clone of the config. If fn returns an error nothing
// is written and the in-memory snapshot is left untouched.
func (s *Store) Update(fn func(*Config) error) error {
	s.mu.Lock()
	next := s.cfg.Clone()
	if err := fn(next); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := next.Validate(); err != nil {
		s.mu.Unlock()
		return err
	}
	next.Revision++
	if err := writeAtomic(s.path, next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.cfg = next
	if st, err := os.Stat(s.path); err == nil {
		s.modTime = st.ModTime()
	}
	watchers := make([]func(*Config), len(s.watchers))
	copy(watchers, s.watchers)
	s.mu.Unlock()

	for _, fn := range watchers {
		fn(next)
	}
	return nil
}

// Save writes the current snapshot to disk.
func (s *Store) Save() error {
	s.mu.RLock()
	cfg := s.cfg
	s.mu.RUnlock()
	return writeAtomic(s.path, cfg)
}

// Watch polls the config file and reloads it when something else edits it, so
// hand edits take effect without a restart. onError reports edits that were
// read but rejected, which would otherwise disappear silently.
func (s *Store) Watch(interval time.Duration, onReload func(*Config), onError func(error)) {
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for range ticker.C {
			st, err := os.Stat(s.path)
			if err != nil {
				continue
			}
			s.mu.RLock()
			unchanged := st.ModTime().Equal(s.modTime)
			s.mu.RUnlock()
			if unchanged {
				continue
			}

			raw, err := os.ReadFile(s.path)
			if err != nil {
				continue
			}
			fresh := Default()
			if err := yaml.Unmarshal(raw, fresh); err != nil {
				if onError != nil {
					onError(fmt.Errorf("配置文件格式错误：%w", err))
				}
				continue
			}
			applyDefaults(fresh)
			if err := fresh.Validate(); err != nil {
				if onError != nil {
					onError(fmt.Errorf("配置文件内容无效：%w", err))
				}
				continue
			}

			s.mu.Lock()
			if fresh.Revision <= s.cfg.Revision {
				// Stale file (our own write raced the stat) - just resync
				// the timestamp so we stop re-reading it.
				s.modTime = st.ModTime()
				s.mu.Unlock()
				continue
			}
			s.cfg = fresh
			s.modTime = st.ModTime()
			s.mu.Unlock()

			if onReload != nil {
				onReload(fresh)
			}
		}
	}()
}

// Clone deep-copies the config through a YAML round trip; the document is tiny
// and this keeps every nested slice/map independent.
func (c *Config) Clone() *Config {
	raw, err := yaml.Marshal(c)
	if err != nil {
		return c
	}
	out := Default()
	if err := yaml.Unmarshal(raw, out); err != nil {
		return c
	}
	applyDefaults(out)
	return out
}

func writeAtomic(path string, cfg *Config) error {
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	header := "# v2h 配置。面板/CLI 保存时会整体重写此文件，注释不会保留。\n" +
		"# 代理用户的密码以明文保存（Xray 认证需要原文），请把本文件权限限制为 0600。\n"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append([]byte(header), raw...), 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// Default returns the config used on first start.
func Default() *Config {
	return &Config{
		Panel: Panel{
			Listen:         "0.0.0.0:9080",
			Admin:          Admin{Username: "admin"},
			SessionHours:   12,
			TokenIPs:       []string{"127.0.0.1/8", "::1/128"},
			TrustedProxies: []string{"127.0.0.1/8", "::1/128"},
			Theme:          "auto",
		},
		Proxy: Proxy{
			HTTP:     Listener{Enabled: true, Listen: "0.0.0.0:8080"},
			SOCKS:    SocksListener{Enabled: true, Listen: "0.0.0.0:1080", UDP: true},
			Sniffing: true,
			Fallback: FallbackReject,
			Timeout:  Duration(300 * time.Second),
		},
		Logs: Logs{
			Level:      "info",
			AccessLog:  true,
			Dir:        "logs",
			MaxSizeMB:  50,
			MaxBackups: 3,
			RingSize:   2000,
			Console:    true,
		},
		Health: Health{
			Enabled:        true,
			ProbeURL:       "https://www.gstatic.com/generate_204",
			Interval:       Duration(30 * time.Second),
			Timeout:        Duration(5 * time.Second),
			Failures:       2,
			Successes:      1,
			SwitchCooldown: Duration(15 * time.Second),
			MaxProbes:      64,
		},
	}
}

// applyDefaults fills in fields that older or hand-written configs may omit.
func applyDefaults(c *Config) {
	def := Default()
	if c.Panel.Listen == "" {
		c.Panel.Listen = def.Panel.Listen
	}
	if strings.TrimSpace(c.Panel.Admin.Username) == "" {
		c.Panel.Admin.Username = def.Panel.Admin.Username
	}
	if c.Panel.SessionHours <= 0 {
		c.Panel.SessionHours = def.Panel.SessionHours
	}
	if c.Panel.Theme == "" {
		c.Panel.Theme = def.Panel.Theme
	}
	if c.Panel.TokenIPs == nil {
		c.Panel.TokenIPs = def.Panel.TokenIPs
	}
	if c.Panel.TrustedProxies == nil {
		c.Panel.TrustedProxies = def.Panel.TrustedProxies
	}
	if c.Proxy.HTTP.Listen == "" {
		c.Proxy.HTTP.Listen = def.Proxy.HTTP.Listen
	}
	if c.Proxy.SOCKS.Listen == "" {
		c.Proxy.SOCKS.Listen = def.Proxy.SOCKS.Listen
	}
	if c.Proxy.Fallback == "" {
		c.Proxy.Fallback = def.Proxy.Fallback
	}
	if c.Proxy.Timeout == 0 {
		c.Proxy.Timeout = def.Proxy.Timeout
	}
	if c.Logs.Level == "" {
		c.Logs.Level = def.Logs.Level
	}
	if c.Logs.Dir == "" {
		c.Logs.Dir = def.Logs.Dir
	}
	if c.Logs.MaxSizeMB <= 0 {
		c.Logs.MaxSizeMB = def.Logs.MaxSizeMB
	}
	if c.Logs.MaxBackups <= 0 {
		c.Logs.MaxBackups = def.Logs.MaxBackups
	}
	if c.Logs.RingSize <= 0 {
		c.Logs.RingSize = def.Logs.RingSize
	}
	if c.Health.Interval <= 0 {
		c.Health.Interval = def.Health.Interval
	}
	if c.Health.Timeout <= 0 {
		c.Health.Timeout = def.Health.Timeout
	}
	if c.Health.ProbeURL == "" {
		c.Health.ProbeURL = def.Health.ProbeURL
	}
	if c.Health.Failures <= 0 {
		c.Health.Failures = def.Health.Failures
	}
	if c.Health.Successes <= 0 {
		c.Health.Successes = def.Health.Successes
	}
	if c.Health.SwitchCooldown <= 0 {
		c.Health.SwitchCooldown = def.Health.SwitchCooldown
	}
	if c.Health.MaxProbes <= 0 {
		c.Health.MaxProbes = def.Health.MaxProbes
	}
	for i := range c.Subscriptions {
		if c.Subscriptions[i].Kind == "" {
			c.Subscriptions[i].Kind = "auto"
		}
		if c.Subscriptions[i].Interval <= 0 {
			c.Subscriptions[i].Interval = Duration(12 * time.Hour)
		}
	}
	for i := range c.Users {
		if c.Users[i].Mode == "" {
			c.Users[i].Mode = ModePriority
		}
		if c.Users[i].Fallback == "" {
			c.Users[i].Fallback = FallbackInherit
		}
	}
}

// Validate fills in anything the caller left out and then rejects configs that
// would produce a broken Xray document. Normalising here means every entry
// point - hand-written files, the panel, the CLI - ends up with the same
// consistent state.
func (c *Config) Validate() error {
	applyDefaults(c)

	if strings.TrimSpace(c.Panel.Listen) == "" {
		return errors.New("panel.listen 不能为空")
	}
	if strings.TrimSpace(c.Panel.Admin.Username) == "" {
		return errors.New("panel.admin.username 不能为空")
	}
	switch c.Proxy.Fallback {
	case FallbackReject, FallbackDirect:
	default:
		return fmt.Errorf("proxy.fallback 只能是 reject 或 direct，收到 %q", c.Proxy.Fallback)
	}
	switch strings.ToLower(c.Logs.Level) {
	case "debug", "info", "warning", "warn", "error", "none":
	default:
		return fmt.Errorf("logs.level 非法：%q", c.Logs.Level)
	}

	subIDs := map[string]bool{}
	subNames := map[string]bool{}
	for i := range c.Subscriptions {
		s := &c.Subscriptions[i]
		if s.Name == "" {
			return fmt.Errorf("第 %d 个订阅缺少名称", i+1)
		}
		if subNames[s.Name] {
			return fmt.Errorf("订阅名称重复：%q", s.Name)
		}
		subNames[s.Name] = true
		switch s.Kind {
		case "auto", "clash", "v2ray":
		default:
			return fmt.Errorf("订阅 %q 的类型非法：%q", s.Name, s.Kind)
		}
		if s.ID == "" {
			s.ID = NewID("sub")
		}
		subIDs[s.ID] = true
	}

	userNames := map[string]bool{}
	ids := map[string]bool{}
	for i := range c.Users {
		u := &c.Users[i]
		if u.Name == "" {
			return fmt.Errorf("第 %d 个用户缺少用户名", i+1)
		}
		if strings.ContainsAny(u.Name, " \t\r\n:") {
			return fmt.Errorf("用户名 %q 不能包含空格或冒号", u.Name)
		}
		if userNames[u.Name] {
			return fmt.Errorf("用户名重复：%q", u.Name)
		}
		userNames[u.Name] = true
		if u.ID == "" {
			u.ID = NewID("usr")
		}
		if ids[u.ID] {
			u.ID = NewID("usr")
		}
		ids[u.ID] = true
		switch u.Mode {
		case ModePriority, ModeAuto, ModeFixed:
		default:
			return fmt.Errorf("用户 %q 的模式非法：%q", u.Name, u.Mode)
		}
		switch u.Fallback {
		case FallbackInherit, FallbackDirect, FallbackReject:
		default:
			return fmt.Errorf("用户 %q 的兜底策略非法：%q", u.Name, u.Fallback)
		}
		if u.Enabled && u.Password == "" {
			return fmt.Errorf("用户 %q 已启用但没有设置密码", u.Name)
		}
		for j := range u.Targets {
			// Accept a subscription name here and normalise it to its id, so a
			// hand-written config file stays readable.
			if name := u.Targets[j].Sub; name != "" && !subIDs[name] {
				if sub := c.FindSub(name); sub != nil {
					u.Targets[j].Sub = sub.ID
				}
			}
			tgt := u.Targets[j]
			if tgt.Sub != "" && !subIDs[tgt.Sub] {
				return fmt.Errorf("用户 %q 的第 %d 个目标引用了不存在的订阅 %q", u.Name, j+1, tgt.Sub)
			}
			if tgt.Node == "" && !tgt.All {
				return fmt.Errorf("用户 %q 的第 %d 个目标既没有指定节点也不是整订阅", u.Name, j+1)
			}
		}
	}
	return nil
}

// LogDir resolves logs.dir against the data directory so a relative setting
// stays inside the data volume. Both the server and the panel use it, which
// keeps a settings change from silently moving the log file elsewhere.
func (c *Config) LogDir(dataDir string) string {
	dir := strings.TrimSpace(c.Logs.Dir)
	if dir == "" {
		return ""
	}
	if filepath.IsAbs(dir) {
		return dir
	}
	return filepath.Join(dataDir, dir)
}

// FindUser returns the user with the given name or ID.
func (c *Config) FindUser(key string) *User {
	for i := range c.Users {
		if c.Users[i].ID == key || c.Users[i].Name == key {
			return &c.Users[i]
		}
	}
	return nil
}

// FindSub returns the subscription with the given ID or name.
func (c *Config) FindSub(key string) *Subscription {
	for i := range c.Subscriptions {
		if c.Subscriptions[i].ID == key || c.Subscriptions[i].Name == key {
			return &c.Subscriptions[i]
		}
	}
	return nil
}

// EffectiveFallback resolves a user's fallback against the global default.
func (c *Config) EffectiveFallback(u *User) string {
	if u.Fallback == FallbackInherit || u.Fallback == "" {
		return c.Proxy.Fallback
	}
	return u.Fallback
}

// bootstrapAdmin sets a starting admin password and returns it so the caller
// can print it exactly once.
func bootstrapAdmin(c *Config) string {
	if c.Panel.Admin.Username == "" {
		c.Panel.Admin.Username = "admin"
	}
	if pw := strings.TrimSpace(os.Getenv("V2H_ADMIN_PASSWORD")); pw != "" {
		c.Panel.Admin.PasswordHash = auth.MustHashPassword(pw)
		return ""
	}
	pw := NewPassword(16)
	c.Panel.Admin.PasswordHash = auth.MustHashPassword(pw)
	return pw
}

// NewID returns a short random identifier with a readable prefix.
func NewID(prefix string) string {
	return prefix + "_" + randomHex(4)
}

// NewToken returns a 32-byte API token.
func NewToken() string { return randomHex(32) }

// NewPassword returns a random password built from an unambiguous alphabet.
func NewPassword(n int) string {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	for i := range buf {
		buf[i] = alphabet[int(buf[i])%len(alphabet)]
	}
	return string(buf)
}

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		panic(err)
	}
	return hex.EncodeToString(buf)
}
