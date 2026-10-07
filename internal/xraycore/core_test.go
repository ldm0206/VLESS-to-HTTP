package xraycore_test

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/proxy"

	"github.com/ldm0206/vless-to-http/internal/node"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// --- helpers -------------------------------------------------------------

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func localAddr(port int) string { return fmt.Sprintf("127.0.0.1:%d", port) }

type collector struct {
	mu      sync.Mutex
	records []xraycore.LogRecord
}

func (c *collector) handle(r xraycore.LogRecord) {
	c.mu.Lock()
	c.records = append(c.records, r)
	c.mu.Unlock()
}

func (c *collector) snapshot() []xraycore.LogRecord {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]xraycore.LogRecord, len(c.records))
	copy(out, c.records)
	return out
}

func proxyClient(port int, user, pass string) *http.Client {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d", user, pass, port))
	return &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   10 * time.Second,
	}
}

func origin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// tlsOrigin is used where a long-lived CONNECT tunnel matters.
func tlsOrigin(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "origin-ok")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func proxyClientTLS(port int, user, pass string) *http.Client {
	proxyURL, _ := url.Parse(fmt.Sprintf("http://%s:%s@127.0.0.1:%d", user, pass, port))
	return &http.Client{
		Transport: &http.Transport{
			Proxy:           http.ProxyURL(proxyURL),
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 10 * time.Second,
	}
}

func start(t *testing.T, plan xraycore.Plan, handler xraycore.LogHandler) *xraycore.Instance {
	t.Helper()
	raw, err := xraycore.BuildJSON(plan)
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	inst, err := xraycore.Start(raw, handler)
	if err != nil {
		t.Fatalf("start core: %v\nconfig:\n%s", err, raw)
	}
	t.Cleanup(func() { inst.Close() })
	return inst
}

func get(t *testing.T, client *http.Client, target string) (int, string, error) {
	t.Helper()
	resp, err := client.Get(target)
	if err != nil {
		return 0, "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body), nil
}

// --- tests ---------------------------------------------------------------

// TestRestartReusesProcess proves a full stop/start cycle works in-process,
// which is how every config change is applied.
func TestRestartReusesProcess(t *testing.T) {
	srv := origin(t)
	port := freePort(t)
	plan := xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(port)},
		Timeout: 300,
		Users: []xraycore.PlanUser{
			{Name: "alice", Password: "pw", OutboundTag: xraycore.DirectTag},
		},
	}
	first := start(t, plan, nil)
	code, body, err := get(t, proxyClient(port, "alice", "pw"), srv.URL)
	if err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("first run: %d %q %v", code, body, err)
	}
	first.Close()

	second := start(t, plan, nil)
	code, body, err = get(t, proxyClient(port, "alice", "pw"), srv.URL)
	if err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("after restart: %d %q %v", code, body, err)
	}
	second.Close()
}

func TestWrongPasswordRejected(t *testing.T) {
	srv := origin(t)
	port := freePort(t)
	start(t, xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(port)},
		Timeout: 300,
		Users:   []xraycore.PlanUser{{Name: "alice", Password: "pw", OutboundTag: xraycore.DirectTag}},
	}, nil)

	code, _, err := get(t, proxyClient(port, "alice", "wrong"), srv.URL)
	if err == nil && code != http.StatusProxyAuthRequired {
		t.Fatalf("expected 407 or a transport error, got %d", code)
	}
}

// TestPerUserRouting is the core promise of the panel: the username picks the
// server. alice is pointed at a blackhole, bob at direct.
func TestPerUserRouting(t *testing.T) {
	srv := origin(t)
	port := freePort(t)
	start(t, xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(port)},
		Timeout: 300,
		Users: []xraycore.PlanUser{
			{Name: "alice", Password: "pw", OutboundTag: xraycore.BlockTag},
			{Name: "bob", Password: "pw", OutboundTag: xraycore.DirectTag},
		},
	}, nil)

	if code, body, err := get(t, proxyClient(port, "bob", "pw"), srv.URL); err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("bob should reach the origin: %d %q %v", code, body, err)
	}

	code, _, err := get(t, proxyClient(port, "alice", "pw"), srv.URL)
	if err == nil && code == 200 {
		t.Fatalf("alice is routed to a blackhole but got a 200")
	}
}

// TestUnknownUserRejected makes sure an account that is not in the config
// cannot fall through to a default outbound.
func TestUnknownUserRejected(t *testing.T) {
	srv := origin(t)
	port := freePort(t)
	start(t, xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(port)},
		Timeout: 300,
		Users:   []xraycore.PlanUser{{Name: "alice", Password: "pw", OutboundTag: xraycore.DirectTag}},
	}, nil)

	if code, _, err := get(t, proxyClient(port, "mallory", "pw"), srv.URL); err == nil && code == 200 {
		t.Fatalf("unknown user got through with %d", code)
	}
}

func TestLogCaptureAndStats(t *testing.T) {
	srv := tlsOrigin(t)
	port := freePort(t)
	rec := &collector{}
	inst := start(t, xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(port)},
		Timeout: 300,
		Users:   []xraycore.PlanUser{{Name: "bob", Password: "pw", OutboundTag: xraycore.DirectTag}},
	}, rec.handle)

	client := proxyClientTLS(port, "bob", "pw")
	if _, _, err := get(t, client, srv.URL); err != nil {
		t.Fatalf("request through proxy: %v", err)
	}

	// On Linux the core splices direct connections and credits the byte
	// counters when the tunnel ends, so an idle connection still reads as
	// zero. Close it before looking at the numbers.
	client.CloseIdleConnections()

	var stats map[string]xraycore.Traffic
	waitUntil := time.Now().Add(3 * time.Second)
	for time.Now().Before(waitUntil) {
		stats = inst.UserTraffic([]string{"bob", "ghost"})
		if stats["bob"].Up > 0 && stats["bob"].Down > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if stats["bob"].Up == 0 || stats["bob"].Down == 0 {
		t.Fatalf("expected traffic in both directions for bob, got %+v", stats["bob"])
	}
	if stats["ghost"].Up != 0 || stats["ghost"].Down != 0 {
		t.Fatalf("ghost user should have no counters, got %+v", stats["ghost"])
	}

	// Access records must reach the handler. They carry no account name (Xray
	// never fills AccessMessage.Email), so the engine attributes them from the
	// detour tag and the online-IP map - that logic is tested separately.
	var sawAccess bool
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range rec.snapshot() {
			if r.Access && r.Status == "accepted" {
				sawAccess = true
				break
			}
		}
		if sawAccess {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !sawAccess {
		for _, r := range rec.snapshot() {
			t.Logf("record: access=%v status=%s severity=%s msg=%s", r.Access, r.Status, r.Severity, r.Msg)
		}
		t.Fatal("no access record captured")
	}
}

// vlessServerNode starts a second Xray instance acting as the upstream VLESS
// server, and returns it as a node the client core can dial. This is the whole
// data path the panel manages, minus a real remote host.
func vlessServerNode(t *testing.T) node.Node {
	t.Helper()
	port := freePort(t)
	uuid := "b831381d-6324-4d53-ad4f-8cda48b30811"

	serverCfg := map[string]any{
		"log": map[string]any{"loglevel": "warning"},
		"inbounds": []any{map[string]any{
			"tag":      "vless-in",
			"listen":   "127.0.0.1",
			"port":     port,
			"protocol": "vless",
			"settings": map[string]any{
				"clients":    []any{map[string]any{"id": uuid, "level": 0, "email": "server"}},
				"decryption": "none",
			},
		}},
		"outbounds": []any{map[string]any{"tag": "direct", "protocol": "freedom"}},
	}
	raw, err := json.Marshal(serverCfg)
	if err != nil {
		t.Fatalf("marshal server config: %v", err)
	}
	if _, err := xraycore.Start(raw, nil); err != nil {
		t.Fatalf("start upstream server: %v", err)
	}
	// The upstream instance lives until the test binary exits; closing it
	// explicitly would race with the client instance's in-flight connections.

	return node.Node{
		Name: "local-vless", Type: "vless", Server: "127.0.0.1", Port: port,
		UUID: uuid, Network: "tcp",
	}
}

// TestVLESSNodeEndToEnd drives the full path: client → panel HTTP proxy →
// per-user routing → VLESS outbound → upstream Xray → origin.
func TestVLESSNodeEndToEnd(t *testing.T) {
	srv := origin(t)
	n := vlessServerNode(t)
	n.Normalize()

	httpPort := freePort(t)
	probePort := freePort(t)
	inst := start(t, xraycore.Plan{
		HTTP:        xraycore.ListenSpec{Enabled: true, Listen: localAddr(httpPort)},
		ProbeListen: localAddr(probePort),
		ProbePass:   "probepass",
		Probes:      []xraycore.PlanProbe{{NodeID: n.ID, Name: n.Name}},
		Nodes:       []node.Node{n},
		Timeout:     300,
		Users: []xraycore.PlanUser{
			{Name: "bob", Password: "pw", OutboundTag: node.OutboundTag(n.ID)},
		},
	}, nil)

	code, body, err := get(t, proxyClient(httpPort, "bob", "pw"), srv.URL)
	if err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("tunnelled request failed: %d %q %v", code, body, err)
	}
	if got := inst.UserTraffic([]string{"bob"})["bob"]; got.Up == 0 || got.Down == 0 {
		t.Fatalf("expected traffic on both directions, got %+v", got)
	}

	// The same node must also be reachable through the health-probe inbound,
	// which is how the prober decides a node is alive.
	dialer, err := proxy.SOCKS5("tcp", localAddr(probePort),
		&proxy.Auth{User: xraycore.ProbeUser(n.ID), Password: "probepass"}, proxy.Direct)
	if err != nil {
		t.Fatalf("socks dialer: %v", err)
	}
	probeClient := &http.Client{
		Transport: &http.Transport{DialContext: dialer.(proxy.ContextDialer).DialContext},
		Timeout:   10 * time.Second,
	}
	if code, body, err := get(t, probeClient, srv.URL); err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("probe path failed: %d %q %v", code, body, err)
	}
}

// TestDeadNodeFallback covers "the account's servers are all unreachable":
// the request must fail rather than leak out directly.
func TestDeadNodeFallback(t *testing.T) {
	srv := origin(t)
	dead := node.Node{
		Name: "dead", Type: "vless", Server: "127.0.0.1", Port: freePort(t),
		UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", Network: "tcp",
	}
	dead.Normalize()

	httpPort := freePort(t)
	start(t, xraycore.Plan{
		HTTP:    xraycore.ListenSpec{Enabled: true, Listen: localAddr(httpPort)},
		Nodes:   []node.Node{dead},
		Timeout: 300,
		Users: []xraycore.PlanUser{
			{Name: "bob", Password: "pw", OutboundTag: node.OutboundTag(dead.ID)},
			{Name: "erin", Password: "pw", OutboundTag: xraycore.DirectTag},
		},
	}, nil)

	if code, _, err := get(t, proxyClient(httpPort, "bob", "pw"), srv.URL); err == nil && code == 200 {
		t.Fatal("request through a dead node should not succeed")
	}
	// Same config, fallback=direct instead of reject: now it must leak out.
	if code, body, err := get(t, proxyClient(httpPort, "erin", "pw"), srv.URL); err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("direct fallback failed: %d %q %v", code, body, err)
	}
}

// TestSocksInbound exercises the SOCKS5 listener with password auth.
func TestSocksInbound(t *testing.T) {
	srv := origin(t)
	socksPort := freePort(t)
	start(t, xraycore.Plan{
		SOCKS:   xraycore.ListenSpec{Enabled: true, Listen: localAddr(socksPort)},
		Timeout: 300,
		Users:   []xraycore.PlanUser{{Name: "carol", Password: "pw", OutboundTag: xraycore.DirectTag}},
	}, nil)

	dialer, err := proxy.SOCKS5("tcp", localAddr(socksPort),
		&proxy.Auth{User: "carol", Password: "pw"}, proxy.Direct)
	if err != nil {
		t.Fatalf("socks dialer: %v", err)
	}
	ctxDialer := dialer.(proxy.ContextDialer)
	client := &http.Client{
		Transport: &http.Transport{DialContext: ctxDialer.DialContext},
		Timeout:   10 * time.Second,
	}
	if code, body, err := get(t, client, srv.URL); err != nil || code != 200 || body != "origin-ok" {
		t.Fatalf("socks request failed: %d %q %v", code, body, err)
	}
}
