package config

import "time"

// Config is the whole persistent state of a v2h instance: it lives in
// <data>/config.yaml and is the single source of truth for the panel, the CLI
// and the running proxy.
type Config struct {
	Panel         Panel          `yaml:"panel" json:"panel"`
	Proxy         Proxy          `yaml:"proxy" json:"proxy"`
	Logs          Logs           `yaml:"logs" json:"logs"`
	Health        Health         `yaml:"health" json:"health"`
	Subscriptions []Subscription `yaml:"subscriptions" json:"subscriptions"`
	Users         []User         `yaml:"users" json:"users"`

	// Revision increments on every successful save so the engine can tell
	// whether it already applied the current state.
	Revision uint64 `yaml:"revision" json:"revision"`
}

// Panel holds everything about the web control panel and its HTTP API.
type Panel struct {
	Listen         string   `yaml:"listen" json:"listen"`
	PublicURL      string   `yaml:"public_url" json:"public_url"`
	Admin          Admin    `yaml:"admin" json:"admin"`
	SessionHours   int      `yaml:"session_hours" json:"session_hours"`
	TokenIPs       []string `yaml:"token_ips" json:"token_ips"`
	TrustedProxies []string `yaml:"trusted_proxies" json:"trusted_proxies"`
	APIToken       string   `yaml:"api_token" json:"api_token"`
	Theme          string   `yaml:"theme" json:"theme"`
}

// Admin is the single management account behind the panel login.
type Admin struct {
	Username     string    `yaml:"username" json:"username"`
	PasswordHash string    `yaml:"password_hash" json:"password_hash"`
	Turnstile    Turnstile `yaml:"turnstile" json:"turnstile"`
}

// Turnstile configures Cloudflare Turnstile verification on the login form.
type Turnstile struct {
	Enabled   bool   `yaml:"enabled" json:"enabled"`
	SiteKey   string `yaml:"site_key" json:"site_key"`
	SecretKey string `yaml:"secret_key" json:"secret_key"`
	// FailOpen keeps logins working when Cloudflare is unreachable. Off by
	// default: a panel exposed to the internet should not lose its bot check.
	FailOpen bool `yaml:"fail_open" json:"fail_open"`
}

// Proxy describes the two inbound listeners clients connect to.
type Proxy struct {
	HTTP        Listener         `yaml:"http" json:"http"`
	SOCKS       SocksListener    `yaml:"socks" json:"socks"`
	Sniffing    bool             `yaml:"sniffing" json:"sniffing"`
	Fallback    string           `yaml:"fallback" json:"fallback"` // reject | direct
	DNSServers  []string         `yaml:"dns_servers" json:"dns_servers"`
	CustomRules []map[string]any `yaml:"custom_rules" json:"custom_rules"`
	Timeout     Duration         `yaml:"timeout" json:"timeout"`
}

// Listener is a plain TCP proxy endpoint.
type Listener struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
}

// SocksListener is a SOCKS5 endpoint, which additionally carries UDP.
type SocksListener struct {
	Enabled bool   `yaml:"enabled" json:"enabled"`
	Listen  string `yaml:"listen" json:"listen"`
	UDP     bool   `yaml:"udp" json:"udp"`
}

// Logs controls the rotating log file and the in-memory ring the panels read.
type Logs struct {
	Level      string `yaml:"level" json:"level"` // debug | info | warning | error | none
	AccessLog  bool   `yaml:"access_log" json:"access_log"`
	Dir        string `yaml:"dir" json:"dir"`
	MaxSizeMB  int    `yaml:"max_size_mb" json:"max_size_mb"`
	MaxBackups int    `yaml:"max_backups" json:"max_backups"`
	RingSize   int    `yaml:"ring_size" json:"ring_size"`
	Console    bool   `yaml:"console" json:"console"`
}

// Health drives the node prober that powers priority failover.
type Health struct {
	Enabled        bool     `yaml:"enabled" json:"enabled"`
	ProbeURL       string   `yaml:"probe_url" json:"probe_url"`
	Interval       Duration `yaml:"interval" json:"interval"`
	Timeout        Duration `yaml:"timeout" json:"timeout"`
	Failures       int      `yaml:"failures" json:"failures"`
	Successes      int      `yaml:"successes" json:"successes"`
	SwitchCooldown Duration `yaml:"switch_cooldown" json:"switch_cooldown"`
	MaxProbes      int      `yaml:"max_probes" json:"max_probes"`
}

// Subscription is a remote Clash or v2ray node list.
type Subscription struct {
	ID         string    `yaml:"id" json:"id"`
	Name       string    `yaml:"name" json:"name"`
	URL        string    `yaml:"url" json:"url"`
	Kind       string    `yaml:"kind" json:"kind"` // auto | clash | v2ray
	Interval   Duration  `yaml:"interval" json:"interval"`
	Enabled    bool      `yaml:"enabled" json:"enabled"`
	UserAgent  string    `yaml:"user_agent" json:"user_agent"`
	LastUpdate time.Time `yaml:"last_update" json:"last_update"`
	LastStatus string    `yaml:"last_status" json:"last_status"` // ok | error | never
	LastError  string    `yaml:"last_error" json:"last_error"`
	NodeCount  int       `yaml:"node_count" json:"node_count"`
	// UserInfo is the quota the provider reported when the subscription was
	// last fetched. It stays empty for local imports and for providers that
	// report nothing.
	UserInfo SubUserInfo `yaml:"user_info" json:"user_info"`
}

// User is a proxy client: one username/password pair plus the servers it may
// reach, in priority order.
type User struct {
	ID        string    `yaml:"id" json:"id"`
	Name      string    `yaml:"name" json:"name"`
	Password  string    `yaml:"password" json:"password"`
	Enabled   bool      `yaml:"enabled" json:"enabled"`
	Mode      string    `yaml:"mode" json:"mode"`         // priority | auto | fixed
	Fallback  string    `yaml:"fallback" json:"fallback"` // inherit | direct | reject
	Targets   []Target  `yaml:"targets" json:"targets"`
	Note      string    `yaml:"note" json:"note"`
	Traffic   Traffic   `yaml:"traffic" json:"traffic"`
	CreatedAt time.Time `yaml:"created_at" json:"created_at"`
}

// Target selects nodes from a subscription for one user.
type Target struct {
	Sub   string `yaml:"sub" json:"sub"`
	Node  string `yaml:"node" json:"node"`
	All   bool   `yaml:"all" json:"all"`
	Limit int    `yaml:"limit" json:"limit"`
}

// Traffic is a byte counter that survives core restarts.
type Traffic struct {
	Up   int64 `yaml:"up" json:"up"`
	Down int64 `yaml:"down" json:"down"`
}

// ResolvedTarget is a Target after node lookup, used by the panel and engine.
type ResolvedTarget struct {
	Sub      string `json:"sub"`
	SubName  string `json:"sub_name"`
	NodeID   string `json:"node_id"`
	NodeName string `json:"node_name"`
	Type     string `json:"type"`
	// Missing marks a target whose node disappeared after a subscription
	// update, so the panel can flag it instead of silently dropping it.
	Missing bool `json:"missing"`
}

// TargetModes and fallbacks, validated on load and on API input.
const (
	ModePriority = "priority"
	ModeAuto     = "auto"
	ModeFixed    = "fixed"

	FallbackInherit = "inherit"
	FallbackDirect  = "direct"
	FallbackReject  = "reject"
)
