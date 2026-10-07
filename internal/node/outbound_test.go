package node

import (
	"encoding/json"
	"strings"
	"testing"
)

// dig walks a decoded JSON object by key path.
func dig(t *testing.T, m map[string]any, keys ...string) any {
	t.Helper()
	var cur any = m
	for _, key := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			t.Fatalf("key %q: not an object (%T)", key, cur)
		}
		cur, ok = obj[key]
		if !ok {
			t.Fatalf("key %q missing; have %v", key, keysOf(obj))
		}
	}
	return cur
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func decode(t *testing.T, n Node) map[string]any {
	t.Helper()
	ob, err := n.Outbound(OutboundTag(n.ID))
	if err != nil {
		t.Fatalf("outbound: %v", err)
	}
	raw, err := json.Marshal(ob)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return out
}

// TestVLESSRealityTCP mirrors the mapping the original shell entrypoint used,
// which is known to work against real servers.
func TestVLESSRealityTCP(t *testing.T) {
	n := Node{
		Name:             "香港01",
		Type:             "vless",
		Server:           "45.127.127.127",
		Port:             443,
		UUID:             "49b4b82b-73f0-4772-86ca-ca5059375c63",
		Flow:             "xtls-rprx-vision",
		Network:          "tcp",
		SNI:              "github.com",
		Fingerprint:      "firefox",
		RealityPublicKey: "6ECfTRNxRBiv7GLIIwOhwlkDs9NyYoZ7lHZrWeU1Q",
		RealityShortID:   "c8aa6a68a476c885",
		RealitySpiderX:   "/",
	}
	n.Normalize()

	ob := decode(t, n)
	if ob["protocol"] != "vless" {
		t.Fatalf("protocol = %v", ob["protocol"])
	}
	user := dig(t, ob, "settings", "vnext").([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["encryption"] != "none" || user["flow"] != "xtls-rprx-vision" {
		t.Fatalf("user block = %v", user)
	}
	if dig(t, ob, "streamSettings", "security") != "reality" {
		t.Fatalf("security = %v", dig(t, ob, "streamSettings", "security"))
	}
	reality := dig(t, ob, "streamSettings", "realitySettings").(map[string]any)
	if reality["publicKey"] != n.RealityPublicKey || reality["shortId"] != "c8aa6a68a476c885" {
		t.Fatalf("realitySettings = %v", reality)
	}
	if reality["serverName"] != "github.com" || reality["fingerprint"] != "firefox" || reality["spiderX"] != "/" {
		t.Fatalf("realitySettings = %v", reality)
	}
}

// TestVLESSXHTTP checks the transport added in the previous iteration of the
// project still renders.
func TestVLESSXHTTP(t *testing.T) {
	n := Node{
		Name: "xhttp", Type: "vless", Server: "example.com", Port: 443,
		UUID:    "49b4b82b-73f0-4772-86ca-ca5059375c63",
		Network: "xhttp", Path: "/my-path", Host: "example.com", XHTTPMode: "auto",
		TLS: true, SNI: "example.com", Fingerprint: "chrome",
	}
	n.Normalize()

	ob := decode(t, n)
	xhttp := dig(t, ob, "streamSettings", "xhttpSettings").(map[string]any)
	if xhttp["path"] != "/my-path" || xhttp["host"] != "example.com" || xhttp["mode"] != "auto" {
		t.Fatalf("xhttpSettings = %v", xhttp)
	}
	if dig(t, ob, "streamSettings", "security") != "tls" {
		t.Fatalf("security = %v", dig(t, ob, "streamSettings", "security"))
	}
}

// TestFlowDroppedOnNonTCP guards against a config the core would refuse to
// start: flow=xtls-rprx-vision only exists on raw TCP.
func TestFlowDroppedOnNonTCP(t *testing.T) {
	n := Node{
		Name: "ws", Type: "vless", Server: "example.com", Port: 443,
		UUID:    "49b4b82b-73f0-4772-86ca-ca5059375c63",
		Network: "ws", Path: "/ws", Flow: "xtls-rprx-vision", TLS: true, SNI: "example.com",
	}
	n.Normalize()

	ob := decode(t, n)
	user := dig(t, ob, "settings", "vnext").([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if _, ok := user["flow"]; ok {
		t.Fatalf("flow should be dropped for ws transport: %v", user)
	}
}

// TestInsecureNodePinsCertificate covers the removal of allowInsecure: the core
// refuses any config that still carries the field, so a subscription asking to
// skip verification is served by pinning the certificate instead.
func TestInsecureNodePinsCertificate(t *testing.T) {
	pin := strings.Repeat("ab", 32)
	pinned := Node{
		Name: "cdn", Type: "vless", Server: "cdn.example.com", Port: 443,
		UUID: "49b4b82b-73f0-4772-86ca-ca5059375c63",
		TLS:  true, SNI: "cdn.example.com", SkipCertVerify: true,
		PinnedCertSha256: pin,
	}
	pinned.Normalize()

	tls := dig(t, decode(t, pinned), "streamSettings", "tlsSettings").(map[string]any)
	if tls["pinnedPeerCertSha256"] != pin {
		t.Fatalf("pin missing from tlsSettings: %v", tls)
	}
	if _, ok := tls["allowInsecure"]; ok {
		t.Fatalf("allowInsecure must never be emitted: %v", tls)
	}

	// Before a pin is resolved the node falls back to normal verification
	// rather than emitting a field the core rejects.
	unpinned := pinned
	unpinned.Name = "cdn-2"
	unpinned.PinnedCertSha256 = ""
	unpinned.Normalize()

	tls = dig(t, decode(t, unpinned), "streamSettings", "tlsSettings").(map[string]any)
	if _, ok := tls["allowInsecure"]; ok {
		t.Fatalf("allowInsecure must never be emitted: %v", tls)
	}
	if _, ok := tls["pinnedPeerCertSha256"]; ok {
		t.Fatalf("nothing should be pinned before the server was inspected: %v", tls)
	}
}

// TestNeedsCertPinOnlyForTLSNodes keeps the engine from probing hosts that will
// never carry a pin: REALITY authenticates the server itself, and a plaintext
// transport has no certificate to pin.
func TestNeedsCertPinOnlyForTLSNodes(t *testing.T) {
	cases := []struct {
		name string
		node Node
		want bool
	}{
		{"insecure vless tls", Node{Type: "vless", TLS: true, SkipCertVerify: true}, true},
		{"insecure trojan", Node{Type: "trojan", SkipCertVerify: true}, true},
		{"verified tls", Node{Type: "vless", TLS: true}, false},
		{"reality", Node{Type: "vless", TLS: true, SkipCertVerify: true, RealityPublicKey: "key"}, false},
		{"plaintext ws", Node{Type: "vless", Network: "ws", SkipCertVerify: true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.node.NeedsCertPin(); got != tc.want {
				t.Fatalf("NeedsCertPin() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestVmessTrojanShadowsocks(t *testing.T) {
	vmess := Node{
		Name: "vmess", Type: "vmess", Server: "jp.example.com", Port: 443,
		UUID: "b831381d-6324-4d53-ad4f-8cda48b30811", AlterID: 0, Cipher: "auto",
		Network: "ws", Path: "/ws", Host: "cdn.example.com", TLS: true, SNI: "cdn.example.com",
	}
	vmess.Normalize()
	ob := decode(t, vmess)
	user := dig(t, ob, "settings", "vnext").([]any)[0].(map[string]any)["users"].([]any)[0].(map[string]any)
	if user["security"] != "auto" || user["alterId"] != float64(0) {
		t.Fatalf("vmess user = %v", user)
	}
	if host := dig(t, ob, "streamSettings", "wsSettings", "host"); host != "cdn.example.com" {
		t.Fatalf("ws host = %v", host)
	}

	trojan := Node{Name: "trojan", Type: "trojan", Server: "us.example.com", Port: 443, Password: "secret", Network: "tcp", SNI: "us.example.com"}
	trojan.Normalize()
	ob = decode(t, trojan)
	server := dig(t, ob, "settings", "servers").([]any)[0].(map[string]any)
	if server["password"] != "secret" {
		t.Fatalf("trojan server = %v", server)
	}
	// Trojan is TLS even when the subscription forgets to say so.
	if dig(t, ob, "streamSettings", "security") != "tls" {
		t.Fatalf("trojan security = %v", dig(t, ob, "streamSettings", "security"))
	}

	ss := Node{Name: "ss", Type: "ss", Server: "sg.example.com", Port: 8388, Cipher: "chacha20-ietf-poly1305", Password: "pw"}
	ss.Normalize()
	ob = decode(t, ss)
	server = dig(t, ob, "settings", "servers").([]any)[0].(map[string]any)
	if server["method"] != "chacha20-ietf-poly1305" || server["password"] != "pw" {
		t.Fatalf("ss server = %v", server)
	}
}

func TestUnsupportedNodesRefuseToBuild(t *testing.T) {
	hy2 := Node{Name: "hy2", Type: "hysteria2", Server: "a.example.com", Port: 443, Password: "x"}
	hy2.Normalize()
	if hy2.Unsupported == "" {
		t.Fatal("hysteria2 should be flagged")
	}
	if _, err := hy2.Outbound("t"); err == nil {
		t.Fatal("building an outbound for hysteria2 must fail")
	}

	missingUUID := Node{Name: "broken", Type: "vless", Server: "a.example.com", Port: 443}
	missingUUID.Normalize()
	if _, err := missingUUID.Outbound("t"); err == nil {
		t.Fatal("vless without a uuid must fail")
	}
}

func TestNodeIDIsStableAcrossRenames(t *testing.T) {
	base := Node{Type: "vless", Server: "a.example.com", Port: 443, UUID: "u", Network: "tcp"}
	first := base
	first.Name = "香港01"
	first.Normalize()
	second := base
	second.Name = "香港一号"
	second.Normalize()

	if first.ID != second.ID {
		t.Fatalf("id should not depend on the display name: %s vs %s", first.ID, second.ID)
	}

	other := base
	other.Port = 8443
	other.Normalize()
	if other.ID == first.ID {
		t.Fatal("id should change when the connection parameters change")
	}
}
