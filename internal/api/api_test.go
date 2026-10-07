package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ldm0206/vless-to-http/internal/auth"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/subscription"
)

const adminPassword = "secret-password"

type testServer struct {
	*httptest.Server
	store  *config.Store
	engine *engine.Engine
	client *http.Client
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	dir := t.TempDir()

	store := config.NewStore(dir)
	if _, err := store.Load(); err != nil {
		t.Fatalf("load store: %v", err)
	}
	hash, err := auth.HashPassword(adminPassword)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	if err := store.Update(func(c *config.Config) error {
		c.Panel.Admin.PasswordHash = hash
		c.Panel.Listen = "127.0.0.1:0"
		c.Proxy.HTTP.Enabled = false
		c.Proxy.SOCKS.Enabled = false
		return nil
	}); err != nil {
		t.Fatalf("seed store: %v", err)
	}

	logger, err := logs.New(logs.Options{RingSize: 200, Level: logs.LevelDebug})
	if err != nil {
		t.Fatalf("logger: %v", err)
	}
	t.Cleanup(logger.Close)

	cache := subscription.NewCache(dir)
	eng := engine.New(store, cache, logger, dir)
	srv, err := New(eng, logger)
	if err != nil {
		t.Fatalf("api: %v", err)
	}

	ts := &testServer{Server: httptest.NewServer(srv.Handler()), store: store, engine: eng}
	ts.client = &http.Client{
		Jar:       nil,
		Transport: ts.Server.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	t.Cleanup(ts.Close)
	return ts
}

// do sends a request, optionally with a session cookie and CSRF header.
func (ts *testServer) do(t *testing.T, method, path, token string, body any, headers ...[2]string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Cookie", CookieName+"="+token)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	resp, err := ts.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return resp
}

func decodeBody(t *testing.T, resp *http.Response, dst any) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(raw, dst); err != nil {
		t.Fatalf("decode %q: %v", string(raw), err)
	}
}

// login authenticates and returns the session cookie value and CSRF token.
func (ts *testServer) login(t *testing.T, password string) (string, string) {
	t.Helper()
	resp := ts.do(t, http.MethodPost, "/api/login", "", map[string]any{
		"username": "admin", "password": password,
	})
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("login failed: %d %s", resp.StatusCode, body)
	}
	var payload struct {
		CSRF string `json:"csrf"`
	}
	decodeBody(t, resp, &payload)

	var cookie string
	for _, c := range ts.lastCookies(resp) {
		if c.Name == CookieName {
			cookie = c.Value
		}
	}
	if cookie == "" {
		t.Fatal("login did not set a session cookie")
	}
	return cookie, payload.CSRF
}

func (ts *testServer) lastCookies(resp *http.Response) []*http.Cookie {
	// Go's http.Response exposes Set-Cookie through the header; parse them.
	raw := resp.Header.Values("Set-Cookie")
	out := make([]*http.Cookie, 0, len(raw))
	for _, line := range raw {
		header := http.Header{"Set-Cookie": {line}}
		resp := &http.Response{Header: header}
		out = append(out, resp.Cookies()...)
	}
	return out
}

func TestHealthIsPublic(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.do(t, http.MethodGet, "/api/health", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health = %d", resp.StatusCode)
	}
}

func TestProtectedEndpointsRequireAuth(t *testing.T) {
	ts := newTestServer(t)
	for _, path := range []string{"/api/status", "/api/users", "/api/subs", "/api/nodes", "/api/logs", "/api/settings"} {
		resp := ts.do(t, http.MethodGet, path, "", nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s = %d, want 401", path, resp.StatusCode)
		}
	}
}

func TestLoginRejectsWrongPassword(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.do(t, http.MethodPost, "/api/login", "", map[string]any{
		"username": "admin", "password": "nope",
	})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong password = %d, want 401", resp.StatusCode)
	}
	// No session cookie may be handed out.
	for _, c := range ts.lastCookies(resp) {
		if c.Name == CookieName && c.Value != "" {
			t.Fatal("a session cookie was issued for a failed login")
		}
	}
}

func TestLoginIsRateLimited(t *testing.T) {
	ts := newTestServer(t)
	var last int
	for i := 0; i < 12; i++ {
		resp := ts.do(t, http.MethodPost, "/api/login", "", map[string]any{
			"username": "admin", "password": "nope",
		})
		last = resp.StatusCode
		resp.Body.Close()
		if last == http.StatusTooManyRequests {
			break
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("brute force was not limited, last status %d", last)
	}
}

func TestSessionAuthAndCSRF(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)

	resp := ts.do(t, http.MethodGet, "/api/status", cookie, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status with session = %d", resp.StatusCode)
	}

	// A mutation without the CSRF header must be refused.
	resp = ts.do(t, http.MethodPost, "/api/users", cookie, map[string]any{"name": "alice", "password": "pw"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("mutation without CSRF = %d, want 403", resp.StatusCode)
	}

	// With the header it goes through.
	resp = ts.do(t, http.MethodPost, "/api/users", cookie,
		map[string]any{"name": "alice", "password": "pw", "targets": []any{}},
		[2]string{"X-CSRF-Token", csrf})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("mutation with CSRF = %d %s", resp.StatusCode, body)
	}
}

func TestAPITokenIsRestrictedToAllowedIPs(t *testing.T) {
	ts := newTestServer(t)
	token := ts.store.Get().Panel.APIToken

	// The default token_ips is loopback, and the test client is loopback.
	resp := ts.do(t, http.MethodGet, "/api/status", "", nil, [2]string{"Authorization", "Bearer " + token})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("token from loopback = %d, want 200", resp.StatusCode)
	}

	// Narrow the allow-list: now the same token must be refused.
	if err := ts.store.Update(func(c *config.Config) error {
		c.Panel.TokenIPs = []string{"10.99.0.0/16"}
		return nil
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	resp = ts.do(t, http.MethodGet, "/api/status", "", nil, [2]string{"Authorization", "Bearer " + token})
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("token from a disallowed range = %d, want 403", resp.StatusCode)
	}
}

func TestForgedSessionCookieIsRejected(t *testing.T) {
	ts := newTestServer(t)
	for _, forged := range []string{"garbage", "eyJ1IjoiYWRtaW4ifQ.deadbeef", ".."} {
		resp := ts.do(t, http.MethodGet, "/api/status", forged, nil)
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("forged cookie %q = %d, want 401", forged, resp.StatusCode)
		}
	}
}

func TestUserLifecycleThroughAPI(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)
	headers := [][2]string{{"X-CSRF-Token", csrf}}

	create := ts.do(t, http.MethodPost, "/api/users", cookie, map[string]any{
		"name": "alice", "password": "pw", "mode": "auto", "fallback": "direct",
		"targets": []any{map[string]any{"sub": "", "all": true}},
	}, headers...)
	if create.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(create.Body)
		create.Body.Close()
		t.Fatalf("create = %d %s", create.StatusCode, body)
	}
	var created struct {
		User engine.UserStatus `json:"user"`
	}
	decodeBody(t, create, &created)
	if created.User.Name != "alice" || created.User.Password != "pw" {
		t.Fatalf("created user = %+v", created.User)
	}

	// Duplicate names are refused.
	dup := ts.do(t, http.MethodPost, "/api/users", cookie, map[string]any{"name": "alice", "password": "pw"}, headers...)
	dup.Body.Close()
	if dup.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate name = %d, want 400", dup.StatusCode)
	}

	// Invalid modes are refused.
	bad := ts.do(t, http.MethodPost, "/api/users", cookie, map[string]any{"name": "bob", "password": "pw", "mode": "warp"}, headers...)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid mode = %d, want 400", bad.StatusCode)
	}

	update := ts.do(t, http.MethodPatch, "/api/users/"+created.User.ID, cookie,
		map[string]any{"enabled": false}, headers...)
	update.Body.Close()
	if update.StatusCode != http.StatusOK {
		t.Fatalf("update = %d", update.StatusCode)
	}
	if u := ts.store.Get().FindUser("alice"); u == nil || u.Enabled {
		t.Fatalf("account was not disabled: %+v", u)
	}

	del := ts.do(t, http.MethodDelete, "/api/users/"+created.User.ID, cookie, nil, headers...)
	del.Body.Close()
	if del.StatusCode != http.StatusOK {
		t.Fatalf("delete = %d", del.StatusCode)
	}
	if u := ts.store.Get().FindUser("alice"); u != nil {
		t.Fatal("account still exists after delete")
	}
}

func TestSubscriptionImportAndNodeListing(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)
	headers := [][2]string{{"X-CSRF-Token", csrf}}

	content := "vless://49b4b82b-73f0-4772-86ca-ca5059375c63@hk.example.com:443?security=reality&sni=github.com&pbk=KEY&sid=abcd&type=tcp#香港01\n" +
		"hysteria2://pw@hy.example.com:443#韩国HY2\n"

	resp := ts.do(t, http.MethodPost, "/api/subs/import", cookie, map[string]any{
		"name": "本地导入", "kind": "v2ray", "content": content,
	}, headers...)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("import = %d %s", resp.StatusCode, body)
	}
	var imported struct {
		Sub engine.SubStatus `json:"sub"`
	}
	decodeBody(t, resp, &imported)
	if imported.Sub.CachedNodes != 2 {
		t.Fatalf("imported %d nodes, want 2", imported.Sub.CachedNodes)
	}
	if imported.Sub.CachedUsable != 1 {
		t.Fatalf("usable = %d, want 1 (hysteria2 is unsupported)", imported.Sub.CachedUsable)
	}

	nodes := ts.do(t, http.MethodGet, "/api/nodes", cookie, nil)
	var listed struct {
		Nodes []engine.NodeStatus `json:"nodes"`
	}
	decodeBody(t, nodes, &listed)
	if len(listed.Nodes) != 2 {
		t.Fatalf("node list = %d", len(listed.Nodes))
	}
	var foundUnsupported bool
	for _, n := range listed.Nodes {
		if strings.Contains(n.Unsupported, "hysteria2") {
			foundUnsupported = true
		}
	}
	if !foundUnsupported {
		t.Fatal("the unsupported node should carry a reason")
	}
}

func TestSettingsPatchAndSecrecy(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)
	headers := [][2]string{{"X-CSRF-Token", csrf}}

	if err := ts.store.Update(func(c *config.Config) error {
		c.Panel.Admin.Turnstile.SecretKey = "top-secret"
		c.Panel.Admin.Turnstile.SiteKey = "site-key"
		return nil
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	resp := ts.do(t, http.MethodGet, "/api/settings", cookie, nil)
	var settings map[string]any
	decodeBody(t, resp, &settings)

	panel, _ := settings["panel"].(map[string]any)
	turnstile, _ := panel["turnstile"].(map[string]any)
	if _, leaked := turnstile["secret_key"]; leaked {
		t.Fatal("the Turnstile secret must not be returned")
	}
	if turnstile["secret_set"] != true || turnstile["site_key"] != "site-key" {
		t.Fatalf("turnstile view = %v", turnstile)
	}

	patch := ts.do(t, http.MethodPatch, "/api/settings", cookie, map[string]any{
		"logs":  map[string]any{"max_size_mb": 123, "level": "warning"},
		"proxy": map[string]any{"fallback": "direct"},
	}, headers...)
	patch.Body.Close()
	if patch.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", patch.StatusCode)
	}
	cfg := ts.store.Get()
	if cfg.Logs.MaxSizeMB != 123 || cfg.Logs.Level != "warning" || cfg.Proxy.Fallback != "direct" {
		t.Fatalf("settings not applied: logs=%+v fallback=%s", cfg.Logs, cfg.Proxy.Fallback)
	}

	// An invalid fallback must be rejected rather than silently stored.
	bad := ts.do(t, http.MethodPatch, "/api/settings", cookie,
		map[string]any{"proxy": map[string]any{"fallback": "whatever"}}, headers...)
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid fallback = %d, want 400", bad.StatusCode)
	}
}

func TestStaticPanelIsServed(t *testing.T) {
	ts := newTestServer(t)
	resp := ts.do(t, http.MethodGet, "/", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("panel index = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content type = %q", ct)
	}
	if resp.Header.Get("Content-Security-Policy") == "" {
		t.Fatal("CSP header missing")
	}
}

func TestLogsEndpoint(t *testing.T) {
	ts := newTestServer(t)
	ts.engine.Logger().Infof("测试日志一")
	ts.engine.Logger().Warnf("测试日志二")

	cookie, _ := ts.login(t, adminPassword)
	resp := ts.do(t, http.MethodGet, "/api/logs?limit=10&level=info", cookie, nil)
	var payload struct {
		Entries []struct {
			Level string `json:"level"`
			Msg   string `json:"msg"`
		} `json:"entries"`
	}
	decodeBody(t, resp, &payload)
	if len(payload.Entries) < 2 {
		t.Fatalf("expected the log lines back, got %+v", payload.Entries)
	}
}

// The config file is the source of truth for the CLI, so make sure the API
// writes it where the CLI expects to find it.
func TestStorePathMatchesDataDir(t *testing.T) {
	ts := newTestServer(t)
	if got := filepath.Base(ts.store.Path()); got != "config.yaml" {
		t.Fatalf("config path base = %q", got)
	}
}

// A relative logs.dir must resolve against the data directory, both at startup
// and when the panel changes logging settings.
func TestLogDirectoryStaysInsideTheDataDir(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)

	resp := ts.do(t, http.MethodPatch, "/api/settings", cookie,
		map[string]any{"logs": map[string]any{"dir": "logs", "max_size_mb": 10}},
		[2]string{"X-CSRF-Token", csrf})
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("patch = %d", resp.StatusCode)
	}

	want := filepath.Join(ts.store.DataDir(), "logs")
	if got := ts.engine.Logger().Options().Dir; got != want {
		t.Fatalf("logger dir = %q, want %q", got, want)
	}

	// An absolute path is honoured as-is.
	abs := t.TempDir()
	resp = ts.do(t, http.MethodPatch, "/api/settings", cookie,
		map[string]any{"logs": map[string]any{"dir": abs}},
		[2]string{"X-CSRF-Token", csrf})
	resp.Body.Close()
	if got := ts.engine.Logger().Options().Dir; got != abs {
		t.Fatalf("logger dir = %q, want %q", got, abs)
	}
}

// Nodes carry the subscription they came from, which is what makes the
// panel's per-subscription filter and 测速 button work.
func TestNodeListExposesSubscription(t *testing.T) {
	ts := newTestServer(t)
	cookie, csrf := ts.login(t, adminPassword)

	imported := ts.do(t, http.MethodPost, "/api/subs/import", cookie, map[string]any{
		"name": "本地导入", "kind": "v2ray",
		"content": "vless://49b4b82b-73f0-4772-86ca-ca5059375c63@hk.example.com:443?security=tls&sni=hk.example.com&type=tcp#香港01\n",
	}, [2]string{"X-CSRF-Token", csrf})
	var sub struct {
		Sub engine.SubStatus `json:"sub"`
	}
	decodeBody(t, imported, &sub)

	resp := ts.do(t, http.MethodGet, "/api/nodes?sub="+sub.Sub.ID, cookie, nil)
	var listed struct {
		Nodes []engine.NodeStatus `json:"nodes"`
	}
	decodeBody(t, resp, &listed)
	if len(listed.Nodes) != 1 {
		t.Fatalf("filtering by subscription returned %d nodes", len(listed.Nodes))
	}
	if listed.Nodes[0].SubID != sub.Sub.ID || listed.Nodes[0].SubName != "本地导入" {
		t.Fatalf("node is missing its subscription: %+v", listed.Nodes[0])
	}

	// The dashboard counters must reflect the same data.
	status := ts.do(t, http.MethodGet, "/api/status", cookie, nil)
	var payload struct {
		Status engine.Status `json:"status"`
	}
	decodeBody(t, status, &payload)
	if payload.Status.NodeSummary.Total != 1 || payload.Status.NodeSummary.Usable != 1 {
		t.Fatalf("node summary = %+v", payload.Status.NodeSummary)
	}
}
