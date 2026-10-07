package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/node"
	"github.com/ldm0206/vless-to-http/internal/subscription"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

const testUUID = "b831381d-6324-4d53-ad4f-8cda48b30811"

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startUpstream runs a minimal Xray VLESS server (no TLS, freedom outbound)
// that plays the role of a subscription's remote server.
func startUpstream(t *testing.T) (port int, stop func()) {
	t.Helper()
	port = freePort(t)
	cfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "vless-in",
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": testUUID, "level": 0, "email": "upstream"}},
				"decryption": "none",
			},
		}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal upstream config: %v", err)
	}
	inst, err := xraycore.Start(raw, nil)
	if err != nil {
		t.Fatalf("start upstream: %v", err)
	}
	return port, func() { inst.Close() }
}

func proxyClient(port int, user, pass string) *http.Client {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d", user, pass, port))
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
}

func fetch(t *testing.T, client *http.Client, target string) (int, string) {
	t.Helper()
	resp, err := client.Get(target)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// writeConfig puts a config document on disk so the store loads it the same
// way a real deployment would.
func writeConfig(t *testing.T, dir string, cfg *config.Config) *config.Store {
	t.Helper()
	raw, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), raw, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	store := config.NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load config: %v", err)
	}
	return store
}

func newTestLogger(t *testing.T) *logs.Logger {
	t.Helper()
	logger, err := logs.New(logs.Options{Level: logs.LevelDebug, RingSize: 500})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(logger.Close)
	return logger
}

// TestEngineProxiesThroughNodeAndFailsOver is the whole feature in one test:
// an account reaches the origin through a real VLESS tunnel, its traffic is
// counted, and when the server disappears the engine switches the account to
// the configured fallback without losing the service.
func TestEngineProxiesThroughNodeAndFailsOver(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	defer origin.Close()

	upstreamPort, stopUpstream := startUpstream(t)
	defer stopUpstream()

	target := node.Node{
		Name: "本地节点", Type: "vless", Server: "127.0.0.1", Port: upstreamPort,
		UUID: testUUID, Network: "tcp",
	}
	target.Normalize()

	dir := t.TempDir()
	httpPort := freePort(t)

	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP = config.Listener{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	cfg.Proxy.SOCKS = config.SocksListener{Enabled: false}
	cfg.Proxy.Fallback = config.FallbackDirect
	cfg.Health.Enabled = true
	cfg.Health.Failures = 1
	cfg.Health.Successes = 1
	cfg.Health.Interval = config.Duration(time.Second)
	cfg.Health.SwitchCooldown = config.Duration(50 * time.Millisecond)
	cfg.Health.Timeout = config.Duration(3 * time.Second)
	cfg.Health.ProbeURL = "https://example.com/"
	cfg.Subscriptions = []config.Subscription{{ID: "sub_1", Name: "本地订阅", Kind: "auto", Enabled: true}}
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true,
		Mode: config.ModePriority, Fallback: config.FallbackInherit,
		Targets: []config.Target{{Sub: "sub_1", Node: target.Name}},
	}}

	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)
	if err := cache.Set("sub_1", []node.Node{target}, "clash"); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	eng := New(store, cache, newTestLogger(t), dir)
	if err := eng.Apply("测试"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer eng.Close()

	route := eng.UserRouteOf("usr_1")
	if route == nil || route.ActiveNode != target.ID {
		t.Fatalf("expected the account to be routed to the node, got %+v", route)
	}

	client := proxyClient(httpPort, "alice", "pw")
	if code, body := fetch(t, client, origin.URL); code != 200 || body != "origin-ok" {
		t.Fatalf("tunnelled request failed: %d %q", code, body)
	}

	// Traffic accounting reads the core's counters.
	eng.pollOnce()
	traffic := eng.Traffic()["usr_1"]
	if traffic.Down == 0 {
		t.Fatalf("downlink traffic was not counted: %+v", traffic)
	}

	// The server goes away: one probe is enough with Failures=1.
	stopUpstream()
	if _, err := eng.Prober().Test(context.Background(), target.ID); err == nil {
		t.Fatal("probe against a dead server should fail")
	}

	deadline := time.Now().Add(5 * time.Second)
	var switched *UserRoute
	for time.Now().Before(deadline) {
		if r := eng.UserRouteOf("usr_1"); r != nil && r.Outbound == xraycore.DirectTag {
			switched = r
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if switched == nil {
		t.Fatalf("account was not moved to the fallback, route = %+v", eng.UserRouteOf("usr_1"))
	}
	if switched.Reason == "" {
		t.Fatal("the fallback should explain itself in the route reason")
	}

	// Service continues through the direct fallback.
	if code, body := fetch(t, client, origin.URL); code != 200 || body != "origin-ok" {
		t.Fatalf("request after failover failed: %d %q", code, body)
	}
}

// TestEngineRejectsWhenFallbackIsReject covers the other fallback policy: an
// account whose servers are all gone must be cut off rather than leak out.
func TestEngineRejectsWhenFallbackIsReject(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	defer origin.Close()

	dir := t.TempDir()
	httpPort := freePort(t)
	deadPort := freePort(t) // nothing listens here

	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP = config.Listener{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	cfg.Proxy.SOCKS = config.SocksListener{Enabled: false}
	cfg.Proxy.Fallback = config.FallbackReject
	cfg.Health.Enabled = true
	cfg.Health.Failures = 1
	cfg.Health.Timeout = config.Duration(2 * time.Second)
	cfg.Health.SwitchCooldown = config.Duration(50 * time.Millisecond)
	cfg.Subscriptions = []config.Subscription{{ID: "sub_1", Name: "本地订阅", Kind: "auto", Enabled: true}}
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true,
		Mode: config.ModePriority, Fallback: config.FallbackInherit,
		Targets: []config.Target{{Sub: "sub_1", Node: "死节点"}},
	}}

	dead := node.Node{Name: "死节点", Type: "vless", Server: "127.0.0.1", Port: deadPort, UUID: testUUID, Network: "tcp"}
	dead.Normalize()

	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)
	if err := cache.Set("sub_1", []node.Node{dead}, "clash"); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	eng := New(store, cache, newTestLogger(t), dir)
	if err := eng.Apply("测试"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer eng.Close()

	// The node is only written off once a probe says so.
	probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
	if _, err := eng.Prober().Test(probeCtx, dead.ID); err == nil {
		cancelProbe()
		t.Fatal("probe against a dead port should fail")
	}
	cancelProbe()

	deadline := time.Now().Add(5 * time.Second)
	var route *UserRoute
	for time.Now().Before(deadline) {
		route = eng.UserRouteOf("usr_1")
		if route != nil && route.Outbound == xraycore.BlockTag {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if route == nil || route.Outbound != xraycore.BlockTag {
		t.Fatalf("expected the account to be blackholed, got %+v", route)
	}
	if got := eng.PlanJSON(); !strings.Contains(got, "blackhole") {
		t.Fatalf("generated config should contain a blackhole outbound")
	}

	client := proxyClient(httpPort, "alice", "pw")
	if code, body := fetch(t, client, origin.URL); code == 200 {
		t.Fatalf("the request should not have gone through, got %d %q", code, body)
	}
}

// TestEngineAppliesUserChanges checks that editing an account takes effect
// without a manual restart.
func TestEngineAppliesUserChanges(t *testing.T) {
	dir := t.TempDir()
	httpPort := freePort(t)

	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP = config.Listener{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	cfg.Proxy.SOCKS = config.SocksListener{Enabled: false}
	cfg.Subscriptions = []config.Subscription{{ID: "sub_1", Name: "本地订阅", Kind: "auto", Enabled: true}}
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true, Mode: config.ModePriority,
		Targets: []config.Target{{Sub: "sub_1", All: true}},
	}}

	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)
	eng := New(store, cache, newTestLogger(t), dir)
	if err := eng.Apply("测试"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	defer eng.Close()

	if len(eng.Status().Users) != 1 || !eng.Status().Users[0].Enabled {
		t.Fatalf("expected one enabled account: %+v", eng.Status().Users)
	}

	// Disabling the account must remove its credentials from the core.
	if err := store.Update(func(c *config.Config) error {
		c.Users[0].Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("update store: %v", err)
	}
	if err := eng.Apply("停用账号"); err != nil {
		t.Fatalf("re-apply: %v", err)
	}

	route := eng.UserRouteOf("usr_1")
	if route == nil || route.Reason != "账号已停用" {
		t.Fatalf("route after disabling = %+v", route)
	}
	if plan := eng.PlanJSON(); len(plan) == 0 {
		t.Fatal("the engine should expose the generated config")
	}
}

// TestEngineDropsNodesTheCoreRejects is the guard against one malformed server
// in a subscription taking the whole proxy down: the core refuses to start
// with it, the engine drops that node and boots with the rest.
func TestEngineDropsNodesTheCoreRejects(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	defer origin.Close()

	upstreamPort, stopUpstream := startUpstream(t)
	defer stopUpstream()

	good := node.Node{
		Name: "好节点", Type: "vless", Server: "127.0.0.1", Port: upstreamPort,
		UUID: testUUID, Network: "tcp",
	}
	good.Normalize()

	// A REALITY public key that is not valid base64 - exactly the kind of
	// truncated placeholder that appears in hand-copied links.
	bad := node.Node{
		Name: "坏节点", Type: "vless", Server: "bad.example.com", Port: 443,
		UUID: testUUID, Network: "tcp", TLS: true, SNI: "bad.example.com",
		RealityPublicKey: "6ECfTRNxRBiv7GLIIwOhwlkDs9NyYoZ7lHZrWeU1Q",
		RealityShortID:   "c8aa6a68a476c885",
	}
	bad.Normalize()
	if bad.Unsupported != "" {
		t.Fatalf("the broken node should look fine until the core tries it: %s", bad.Unsupported)
	}

	dir := t.TempDir()
	httpPort := freePort(t)

	cfg := config.Default()
	cfg.Panel.Listen = "127.0.0.1:0"
	cfg.Proxy.HTTP = config.Listener{Enabled: true, Listen: fmt.Sprintf("127.0.0.1:%d", httpPort)}
	cfg.Proxy.SOCKS = config.SocksListener{Enabled: false}
	cfg.Health.Enabled = false
	cfg.Subscriptions = []config.Subscription{{ID: "sub_1", Name: "本地订阅", Kind: "auto", Enabled: true}}
	cfg.Users = []config.User{{
		ID: "usr_1", Name: "alice", Password: "pw", Enabled: true, Mode: config.ModePriority,
		Targets: []config.Target{
			{Sub: "sub_1", Node: bad.Name},
			{Sub: "sub_1", Node: good.Name},
		},
	}}

	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)
	if err := cache.Set("sub_1", []node.Node{bad, good}, "clash"); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	eng := New(store, cache, newTestLogger(t), dir)
	if err := eng.Apply("测试"); err != nil {
		t.Fatalf("apply should survive a rejected node: %v", err)
	}
	defer eng.Close()

	if broken := eng.BrokenNodes(); broken[bad.ID] == "" {
		t.Fatalf("the rejected node was not recorded: %+v", broken)
	}

	route := eng.UserRouteOf("usr_1")
	if route == nil || route.ActiveNode != good.ID {
		t.Fatalf("traffic should have moved to the working node, got %+v", route)
	}
	if len(route.Missing) == 0 {
		t.Fatal("the rejected node should be reported to the operator")
	}

	// The core is up and serving through the surviving node.
	client := proxyClient(httpPort, "alice", "pw")
	if code, body := fetch(t, client, origin.URL); code != 200 || body != "origin-ok" {
		t.Fatalf("proxy request failed: %d %q", code, body)
	}

	// The node table explains why it was skipped.
	for _, status := range eng.Nodes() {
		if status.ID == bad.ID && !strings.Contains(status.Unsupported, "内核拒绝") {
			t.Fatalf("node status does not explain the rejection: %+v", status)
		}
	}
}

// TestAccessLogAttribution covers the two ways an access line gets a name:
// a sticky client-IP map, and the outbound tag when only one account can use
// it. Xray never fills AccessMessage.Email, so this is the only attribution
// the panels have.
func TestAccessLogAttribution(t *testing.T) {
	e := &Engine{
		ipUser:  map[string]ipOwner{},
		tagUser: map[string][]string{},
	}

	single := xraycore.LogRecord{
		Access: true,
		From:   "203.0.113.7:51000",
		Msg:    "from 203.0.113.7:51000 accepted tcp:example.com:443 [http-in -> node:aaa]",
	}
	e.tagUser["node:aaa"] = []string{"alice"}
	if got := e.attribute(single); got != "alice" {
		t.Fatalf("single-user tag should attribute, got %q", got)
	}

	shared := xraycore.LogRecord{
		Access: true,
		From:   "203.0.113.8:51000",
		Msg:    "from 203.0.113.8:51000 accepted tcp:example.com:443 [http-in -> node:bbb]",
	}
	e.tagUser["node:bbb"] = []string{"alice", "bob"}
	if got := e.attribute(shared); got != "" {
		t.Fatalf("a tag shared by two accounts must stay unattributed, got %q", got)
	}

	// A known client IP wins, which is what makes shared nodes attributable.
	e.ipUser["203.0.113.8"] = ipOwner{user: "bob", seen: time.Now()}
	if got := e.attribute(shared); got != "bob" {
		t.Fatalf("sticky IP map should attribute, got %q", got)
	}

	// Entries older than the TTL are ignored.
	e.ipUser["203.0.113.8"] = ipOwner{user: "bob", seen: time.Now().Add(-time.Hour)}
	if got := e.attribute(shared); got != "" {
		t.Fatalf("stale IP entries must expire, got %q", got)
	}
}
