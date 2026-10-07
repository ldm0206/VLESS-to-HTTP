package engine

import (
	"strings"
	"testing"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/node"
)

func testNode(name, server string) node.Node {
	n := node.Node{
		Name: name, Type: "vless", Server: server, Port: 443,
		UUID: "49b4b82b-73f0-4772-86ca-ca5059375c63", Network: "tcp",
	}
	n.Normalize()
	return n
}

func testConfig(user config.User) *config.Config {
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{ID: "sub_a", Name: "机场A", Kind: "auto", Enabled: true}}
	user.ID = "usr_1"
	if user.Mode == "" {
		user.Mode = config.ModePriority
	}
	if user.Fallback == "" {
		user.Fallback = config.FallbackInherit
	}
	cfg.Users = []config.User{user}
	return cfg
}

func resolveWith(t *testing.T, cfg *config.Config, nodes []node.Node, dead map[string]bool) *resolution {
	t.Helper()
	subs := map[string][]node.Node{"sub_a": nodes}
	healthy := func(id string) bool { return !dead[id] }
	return resolve(cfg, subs, healthy, nil, "127.0.0.1:1", "pw")
}

func TestPriorityPicksFirstHealthy(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1"), testNode("日本02", "jp2"), testNode("美国03", "us3")}
	cfg := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true,
		Targets: []config.Target{
			{Sub: "sub_a", Node: "香港01"},
			{Sub: "sub_a", Node: "日本02"},
			{Sub: "sub_a", Node: "美国03"},
		},
	})

	res := resolveWith(t, cfg, nodes, nil)
	route := res.Routes["usr_1"]
	if route.ActiveNode != nodes[0].ID {
		t.Fatalf("expected the first node, got %s", route.ActiveNode)
	}
	if route.Outbound != node.OutboundTag(nodes[0].ID) {
		t.Fatalf("outbound = %s", route.Outbound)
	}
	if len(res.Plan.Users) != 1 || res.Plan.Users[0].OutboundTag != route.Outbound {
		t.Fatalf("plan users = %+v", res.Plan.Users)
	}

	// The first node dies: the second must take over.
	res = resolveWith(t, cfg, nodes, map[string]bool{nodes[0].ID: true})
	if got := res.Routes["usr_1"].ActiveNode; got != nodes[1].ID {
		t.Fatalf("expected failover to node 2, got %s", got)
	}
}

func TestAllNodesDeadFallsBack(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1"), testNode("日本02", "jp2")}
	dead := map[string]bool{nodes[0].ID: true, nodes[1].ID: true}

	reject := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true, Fallback: config.FallbackReject,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}, {Sub: "sub_a", Node: "日本02"}},
	})
	res := resolveWith(t, reject, nodes, dead)
	if got := res.Routes["usr_1"].Outbound; got != "block" {
		t.Fatalf("reject fallback should route to the blackhole, got %q", got)
	}
	if res.Plan.Users[0].OutboundTag != "block" {
		t.Fatalf("plan user outbound = %q", res.Plan.Users[0].OutboundTag)
	}

	direct := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true, Fallback: config.FallbackDirect,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}},
	})
	res = resolveWith(t, direct, nodes, dead)
	if got := res.Routes["usr_1"].Outbound; got != "direct" {
		t.Fatalf("direct fallback should route to freedom, got %q", got)
	}
}

// Inherit means "use proxy.fallback from the config".
func TestFallbackInheritsGlobal(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1")}
	cfg := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true, Fallback: config.FallbackInherit,
		Targets: []config.Target{{Sub: "sub_a", Node: "不存在的节点"}},
	})
	cfg.Proxy.Fallback = config.FallbackDirect
	res := resolveWith(t, cfg, nodes, nil)
	if got := res.Routes["usr_1"].Outbound; got != "direct" {
		t.Fatalf("outbound = %q, want direct", got)
	}
	if len(res.Routes["usr_1"].Missing) == 0 {
		t.Fatal("the missing target should be reported")
	}
}

func TestAutoModeUsesBalancer(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1"), testNode("日本02", "jp2")}
	cfg := testConfig(config.User{
		Name: "bob", Password: "pw", Enabled: true, Mode: config.ModeAuto,
		Targets: []config.Target{{Sub: "sub_a", All: true}},
	})

	res := resolveWith(t, cfg, nodes, map[string]bool{nodes[0].ID: true})
	route := res.Routes["usr_1"]
	if route.Balancer == "" {
		t.Fatal("auto mode must use a balancer")
	}
	if route.ActiveNode != "" || route.Outbound != "" {
		t.Fatalf("auto mode should not pin a node: %+v", route)
	}
	if len(res.Plan.Balancers) != 1 {
		t.Fatalf("balancers = %+v", res.Plan.Balancers)
	}
	balancer := res.Plan.Balancers[0]
	if len(balancer.Selector) != 2 {
		t.Fatalf("selector = %v", balancer.Selector)
	}
	if balancer.Fallback != "block" {
		t.Fatalf("fallback = %q, want block (the default is reject)", balancer.Fallback)
	}
	// The observatory only probes nodes that participate in a balancer, and a
	// dead node must not be excluded from the selector - the core decides.
	if res.Plan.Observatory == nil || len(res.Plan.Observatory.Selector) != 2 {
		t.Fatalf("observatory = %+v", res.Plan.Observatory)
	}
}

func TestFixedModeIgnoresHealth(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1"), testNode("日本02", "jp2")}
	cfg := testConfig(config.User{
		Name: "carol", Password: "pw", Enabled: true, Mode: config.ModeFixed,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}, {Sub: "sub_a", Node: "日本02"}},
	})

	res := resolveWith(t, cfg, nodes, map[string]bool{nodes[0].ID: true})
	if got := res.Routes["usr_1"].ActiveNode; got != nodes[0].ID {
		t.Fatalf("fixed mode must keep the first node even when it is down, got %s", got)
	}
}

func TestWholeSubscriptionTargetWithLimit(t *testing.T) {
	nodes := []node.Node{
		testNode("a", "a.example.com"),
		testNode("b", "b.example.com"),
		testNode("c", "c.example.com"),
	}
	cfg := testConfig(config.User{
		Name: "dave", Password: "pw", Enabled: true,
		Targets: []config.Target{{Sub: "sub_a", All: true, Limit: 2}},
	})
	res := resolveWith(t, cfg, nodes, nil)
	if got := len(res.Routes["usr_1"].NodeIDs); got != 2 {
		t.Fatalf("limit ignored: resolved %d nodes", got)
	}
	if len(res.UsedNodes) != 2 {
		t.Fatalf("used nodes = %d", len(res.UsedNodes))
	}
}

func TestDisabledUserGetsNoAccount(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1")}
	cfg := testConfig(config.User{
		Name: "eve", Password: "pw", Enabled: false,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}},
	})
	res := resolveWith(t, cfg, nodes, nil)
	if len(res.Plan.Users) != 0 {
		t.Fatalf("a disabled account must not be able to authenticate: %+v", res.Plan.Users)
	}
	if len(res.UsedNodes) != 0 {
		t.Fatalf("a disabled account should not materialise nodes: %+v", res.UsedNodes)
	}
}

func TestUnsupportedNodesAreSkipped(t *testing.T) {
	hy2 := node.Node{Name: "hy2", Type: "hysteria2", Server: "h.example.com", Port: 443, Password: "x"}
	hy2.Normalize()
	nodes := []node.Node{hy2, testNode("可用节点", "ok.example.com")}

	cfg := testConfig(config.User{
		Name: "frank", Password: "pw", Enabled: true,
		Targets: []config.Target{
			{Sub: "sub_a", Node: "hy2"},
			{Sub: "sub_a", Node: "可用节点"},
		},
	})
	res := resolveWith(t, cfg, nodes, nil)
	route := res.Routes["usr_1"]
	if len(route.Missing) == 0 {
		t.Fatal("the unsupported node should be reported as unusable")
	}
	if len(res.UsedNodes) != 1 || res.UsedNodes[0].Name != "可用节点" {
		t.Fatalf("used nodes = %+v", res.UsedNodes)
	}
	if route.ActiveNode == "" {
		t.Fatal("the usable node should have been picked")
	}
}

func TestProbeListIsCapped(t *testing.T) {
	nodes := make([]node.Node, 0, 5)
	for _, name := range []string{"n1", "n2", "n3", "n4", "n5"} {
		nodes = append(nodes, testNode(name, name+".example.com"))
	}
	cfg := testConfig(config.User{
		Name: "gina", Password: "pw", Enabled: true,
		Targets: []config.Target{{Sub: "sub_a", All: true}},
	})
	cfg.Health.MaxProbes = 3

	res := resolveWith(t, cfg, nodes, nil)
	if len(res.ProbeNodes) != 3 {
		t.Fatalf("probe list = %v, want 3", res.ProbeNodes)
	}
	if len(res.Plan.Probes) != 3 {
		t.Fatalf("plan probes = %d", len(res.Plan.Probes))
	}
	// All five nodes still get outbounds: the cap only limits health probing.
	if len(res.UsedNodes) != 5 {
		t.Fatalf("used nodes = %d", len(res.UsedNodes))
	}
}

func TestTagUserMapForLogAttribution(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1"), testNode("日本02", "jp2")}
	cfg := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}},
	})

	res := resolveWith(t, cfg, nodes, nil)
	active := node.OutboundTag(nodes[0].ID)
	users := res.TagUsers[active]
	if len(users) != 1 || users[0] != "alice" {
		t.Fatalf("tag users = %v", res.TagUsers)
	}
	if _, ok := res.TagUsers[node.OutboundTag(nodes[1].ID)]; ok {
		t.Fatal("a node that is not in use should not be attributed to anyone")
	}
}

// Custom splitting rules run before per-account routing, which is what makes
// "send this domain direct, everything else through the account's node" work.
func TestCustomRulesPrecedeUserRouting(t *testing.T) {
	nodes := []node.Node{testNode("香港01", "hk1")}
	cfg := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true,
		Targets: []config.Target{{Sub: "sub_a", Node: "香港01"}},
	})
	cfg.Proxy.CustomRules = []map[string]any{
		{"type": "field", "domain": []any{"geosite:private"}, "outboundTag": "direct"},
	}

	res := resolveWith(t, cfg, nodes, nil)
	rules := res.Plan.CustomRules
	if len(rules) != 1 || rules[0]["outboundTag"] != "direct" {
		t.Fatalf("custom rules were dropped: %+v", rules)
	}
	if res.Plan.Users[0].OutboundTag == "direct" {
		t.Fatal("the account should still use its own node")
	}
}

// A node the core already rejected must never be offered again.
func TestBrokenNodesAreSkipped(t *testing.T) {
	nodes := []node.Node{testNode("坏节点", "bad.example.com"), testNode("好节点", "good.example.com")}
	cfg := testConfig(config.User{
		Name: "alice", Password: "pw", Enabled: true,
		Targets: []config.Target{
			{Sub: "sub_a", Node: "坏节点"},
			{Sub: "sub_a", Node: "好节点"},
		},
	})

	subs := map[string][]node.Node{"sub_a": nodes}
	res := resolve(cfg, subs, func(string) bool { return true },
		map[string]string{nodes[0].ID: `invalid "password"`}, "127.0.0.1:1", "pw")

	route := res.Routes["usr_1"]
	if route.ActiveNode != nodes[1].ID {
		t.Fatalf("the working node should be used, got %+v", route)
	}
	if len(route.Missing) != 1 || !strings.Contains(route.Missing[0], "内核拒绝") {
		t.Fatalf("the rejection should be reported: %v", route.Missing)
	}
	if len(res.UsedNodes) != 1 {
		t.Fatalf("a rejected node must not be materialised: %+v", res.UsedNodes)
	}
}
