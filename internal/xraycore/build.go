package xraycore

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ldm0206/vless-to-http/internal/node"
)

// Tag names used across the generated routes.
const (
	DirectTag  = "direct"
	BlockTag   = "block"
	HTTPInTag  = "http-in"
	SocksInTag = "socks-in"
	ProbeInTag = "probe-in"
)

// PlanUser is one authenticated proxy client as the core must see it.
type PlanUser struct {
	Name        string
	Password    string
	OutboundTag string // used unless BalancerTag is set
	BalancerTag string
}

// PlanBalancer is a least-ping group used by auto-mode users.
type PlanBalancer struct {
	Tag      string
	Selector []string
	Fallback string
}

// PlanProbe is a node the engine wants to health-check through the core.
type PlanProbe struct {
	NodeID string
	Name   string
}

// Observatory configures Xray's background health probing for balancers.
type Observatory struct {
	Selector []string
	URL      string
	Interval string
	Timeout  string
	Sampling int
}

// Plan is everything the config builder needs; the engine fills it in.
type Plan struct {
	HTTP        ListenSpec
	SOCKS       ListenSpec
	SocksUDP    bool
	Sniffing    bool
	Timeout     int
	DNSServers  []string
	CustomRules []map[string]any

	ProbeListen string
	ProbePass   string
	Probes      []PlanProbe

	Nodes     []node.Node
	Users     []PlanUser
	Balancers []PlanBalancer
	Fallback  string

	Observatory *Observatory
}

// ListenSpec is one inbound endpoint.
type ListenSpec struct {
	Enabled bool
	Listen  string
}

// BuildJSON renders the plan as an Xray config document.
func BuildJSON(p Plan) ([]byte, error) {
	cfg := map[string]any{
		// The core's own writers stay off; every message is captured by the
		// in-process handler registered right after start.
		"log":       map[string]any{"loglevel": "warning", "access": "none", "error": "none"},
		"stats":     map[string]any{},
		"policy":    policySection(p.Timeout),
		"inbounds":  p.inbounds(),
		"outbounds": p.outbounds(),
		"routing":   p.routing(),
	}
	if len(p.DNSServers) > 0 {
		servers := make([]any, 0, len(p.DNSServers))
		for _, s := range p.DNSServers {
			servers = append(servers, s)
		}
		cfg["dns"] = map[string]any{"servers": servers, "queryStrategy": "UseIP"}
	}
	if p.Observatory != nil && len(p.Observatory.Selector) > 0 {
		cfg["burstObservatory"] = map[string]any{
			"subjectSelector": p.Observatory.Selector,
			"pingConfig": map[string]any{
				"destination": p.Observatory.URL,
				"interval":    p.Observatory.Interval,
				"timeout":     p.Observatory.Timeout,
				"sampling":    p.Observatory.Sampling,
				"httpMethod":  "HEAD",
			},
		}
	}
	return json.MarshalIndent(cfg, "", "  ")
}

func policySection(timeout int) map[string]any {
	if timeout <= 0 {
		timeout = 300
	}
	return map[string]any{
		"levels": map[string]any{
			"0": map[string]any{
				"handshake":         4,
				"connIdle":          timeout,
				"uplinkOnly":        2,
				"downlinkOnly":      5,
				"statsUserUplink":   true,
				"statsUserDownlink": true,
				// Gives us the client IPs behind each account, which is how
				// access-log lines get attributed to a user: Xray does not
				// put the account into the access message itself.
				"statsUserOnline": true,
			},
		},
		"system": map[string]any{
			"statsInboundUplink":   false,
			"statsInboundDownlink": false,
		},
	}
}

func (p Plan) inbounds() []any {
	out := make([]any, 0, 3)

	accounts := make([]any, 0, len(p.Users))
	for _, u := range p.Users {
		if u.Password == "" {
			continue
		}
		accounts = append(accounts, map[string]any{"user": u.Name, "pass": u.Password})
	}

	if p.HTTP.Enabled && len(accounts) > 0 {
		inbound := map[string]any{
			"tag":      HTTPInTag,
			"listen":   hostOf(p.HTTP.Listen),
			"port":     portOf(p.HTTP.Listen),
			"protocol": "http",
			"settings": map[string]any{
				"accounts":  accounts,
				"timeout":   p.Timeout,
				"userLevel": 0,
			},
		}
		if sniffer := p.sniffing(); sniffer != nil {
			inbound["sniffing"] = sniffer
		}
		out = append(out, inbound)
	}

	if p.SOCKS.Enabled && len(accounts) > 0 {
		inbound := map[string]any{
			"tag":      SocksInTag,
			"listen":   hostOf(p.SOCKS.Listen),
			"port":     portOf(p.SOCKS.Listen),
			"protocol": "socks",
			"settings": map[string]any{
				"auth":      "password",
				"accounts":  accounts,
				"udp":       p.SocksUDP,
				"userLevel": 0,
			},
		}
		if sniffer := p.sniffing(); sniffer != nil {
			inbound["sniffing"] = sniffer
		}
		out = append(out, inbound)
	}

	// One loopback inbound carries every health probe: the account name picks
	// the outbound to test, so N nodes still cost a single listener.
	if p.ProbeListen != "" && len(p.Probes) > 0 {
		probeAccounts := make([]any, 0, len(p.Probes))
		for _, probe := range p.Probes {
			probeAccounts = append(probeAccounts, map[string]any{
				"user": ProbeUser(probe.NodeID),
				"pass": p.ProbePass,
			})
		}
		out = append(out, map[string]any{
			"tag":      ProbeInTag,
			"listen":   hostOf(p.ProbeListen),
			"port":     portOf(p.ProbeListen),
			"protocol": "socks",
			"settings": map[string]any{
				"auth":      "password",
				"accounts":  probeAccounts,
				"udp":       false,
				"userLevel": 0,
			},
		})
	}

	return out
}

// sniffing derives the sniffing block from the plan. routeOnly keeps the
// original destination for the outbound while still letting domain rules and
// the access log see the real host name.
func (p Plan) sniffing() map[string]any {
	if !p.Sniffing {
		return nil
	}
	return map[string]any{
		"enabled":      true,
		"destOverride": []any{"http", "tls", "quic"},
		"routeOnly":    true,
	}
}

func (p Plan) outbounds() []any {
	out := make([]any, 0, len(p.Nodes)+2)

	for i := range p.Nodes {
		n := p.Nodes[i]
		ob, err := n.Outbound(node.OutboundTag(n.ID))
		if err != nil {
			continue
		}
		out = append(out, ob)
	}

	direct := map[string]any{
		"tag":      DirectTag,
		"protocol": "freedom",
		"settings": map[string]any{},
	}
	if len(p.DNSServers) > 0 {
		direct["settings"] = map[string]any{"domainStrategy": "UseIP"}
	}
	out = append(out, direct)
	out = append(out, map[string]any{
		"tag":      BlockTag,
		"protocol": "blackhole",
		"settings": map[string]any{},
	})
	return out
}

func (p Plan) routing() map[string]any {
	rules := make([]any, 0, len(p.Probes)+len(p.Users)+len(p.CustomRules)+2)

	// Probe traffic first: these connections are unauthenticated users of the
	// loopback inbound and must never fall through to a user rule.
	for _, probe := range p.Probes {
		rules = append(rules, map[string]any{
			"type":        "field",
			"user":        []any{ProbeUser(probe.NodeID)},
			"outboundTag": node.OutboundTag(probe.NodeID),
		})
	}

	// Operator-supplied splitting rules act before per-user routing so a
	// domain rule can steer traffic away from a user's node.
	for _, rule := range p.CustomRules {
		rules = append(rules, rule)
	}

	for _, u := range p.Users {
		rule := map[string]any{
			"type": "field",
			"user": []any{u.Name},
		}
		if u.BalancerTag != "" {
			rule["balancerTag"] = u.BalancerTag
		} else {
			rule["outboundTag"] = u.OutboundTag
		}
		rules = append(rules, rule)
	}

	// Nothing should reach this, but a failing auth path must not silently
	// become a direct connection.
	rules = append(rules, map[string]any{
		"type":        "field",
		"network":     "tcp,udp",
		"outboundTag": BlockTag,
	})

	routing := map[string]any{
		"domainStrategy": "AsIs",
		"rules":          rules,
	}

	if len(p.Balancers) > 0 {
		balancers := make([]any, 0, len(p.Balancers))
		for _, b := range p.Balancers {
			selector := make([]any, 0, len(b.Selector))
			selector = append(selector, toAny(b.Selector)...)
			balancer := map[string]any{
				"tag":      b.Tag,
				"selector": selector,
				"strategy": map[string]any{"type": "leastPing"},
			}
			if b.Fallback != "" {
				balancer["fallbackTag"] = b.Fallback
			}
			balancers = append(balancers, balancer)
		}
		routing["balancers"] = balancers
	}

	return routing
}

// ProbeUser is the SOCKS account name that selects a node for health probing.
func ProbeUser(nodeID string) string { return "p_" + nodeID }

// ProbeAccount returns the credentials the prober dials with.
func ProbeAccount(nodeID string) (string, string) { return ProbeUser(nodeID), "" }

func toAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func hostOf(listen string) string {
	host, _, err := splitListen(listen)
	if err != nil {
		return "0.0.0.0"
	}
	return host
}

func portOf(listen string) int {
	_, port, err := splitListen(listen)
	if err != nil {
		return 0
	}
	return port
}

// splitListen parses "host:port" including bare ":port" and bracketed IPv6.
func splitListen(listen string) (string, int, error) {
	host, port := "", 0
	if len(listen) > 0 && listen[0] == ':' {
		host = "0.0.0.0"
		fmt.Sscanf(listen[1:], "%d", &port)
	} else {
		h, p := splitHostPort(listen)
		host, port = h, p
	}
	if port <= 0 || port > 65535 {
		return "", 0, fmt.Errorf("非法监听地址 %q", listen)
	}
	return host, port, nil
}

func splitHostPort(s string) (string, int) {
	if len(s) > 0 && s[0] == '[' {
		for i := 0; i < len(s); i++ {
			if s[i] == ']' {
				port := 0
				if i+2 <= len(s) {
					fmt.Sscanf(s[i+2:], "%d", &port)
				}
				return s[1:i], port
			}
		}
	}
	idx := -1
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			idx = i
			break
		}
	}
	if idx < 0 {
		return s, 0
	}
	port := 0
	fmt.Sscanf(s[idx+1:], "%d", &port)
	return s[:idx], port
}

// SortNodes orders nodes deterministically so rebuilding the config twice
// from the same state produces byte-identical JSON.
func SortNodes(nodes []node.Node) {
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].ID < nodes[j].ID })
}
