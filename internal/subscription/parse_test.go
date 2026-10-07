package subscription

import (
	"encoding/base64"
	"testing"

	"github.com/ldm0206/vless-to-http/internal/node"
)

const clashFixture = `
port: 7890
proxies:
  - name: "香港 01"
    type: vless
    server: hk1.example.com
    port: 443
    uuid: 49b4b82b-73f0-4772-86ca-ca5059375c63
    network: tcp
    tls: true
    udp: true
    flow: xtls-rprx-vision
    servername: github.com
    client-fingerprint: chrome
    reality-opts:
      public-key: 6ECfTRNxRBiv7GLIIwOhwlkDs9NyYoZ7lHZrWeU1Q
      short-id: c8aa6a68a476c885
  - name: "日本 02"
    type: vmess
    server: jp2.example.com
    port: 8443
    uuid: b831381d-6324-4d53-ad4f-8cda48b30811
    alterId: 0
    cipher: auto
    tls: true
    network: ws
    ws-opts:
      path: /ws
      headers:
        Host: cdn.example.com
  - name: "美国 Trojan"
    type: trojan
    server: us3.example.com
    port: 443
    password: trojan-pass
    sni: us3.example.com
    skip-cert-verify: true
  - name: "新加坡 SS"
    type: ss
    server: sg4.example.com
    port: 8388
    cipher: chacha20-ietf-poly1305
    password: ss-pass
  - name: "香港 HY2"
    type: hysteria2
    server: hk5.example.com
    port: 443
    password: hy2-pass
  - name: "老加密 SS"
    type: ss
    server: old6.example.com
    port: 8388
    cipher: aes-128-cfb
    password: old-pass
  - name: "带插件的 SS"
    type: ss
    server: plug7.example.com
    port: 8388
    cipher: aes-256-gcm
    password: plug-pass
    plugin: obfs
    plugin-opts:
      mode: tls
`

func TestParseClash(t *testing.T) {
	result, err := Parse([]byte(clashFixture), "auto")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Format != "clash" {
		t.Fatalf("format = %q, want clash", result.Format)
	}
	if len(result.Nodes) != 7 {
		t.Fatalf("got %d nodes, want 7", len(result.Nodes))
	}

	hk := findNode(t, result, "香港 01")
	if hk.Type != "vless" || hk.Server != "hk1.example.com" || hk.Port != 443 {
		t.Fatalf("vless node parsed wrong: %+v", hk)
	}
	if hk.RealityPublicKey != "6ECfTRNxRBiv7GLIIwOhwlkDs9NyYoZ7lHZrWeU1Q" || hk.RealityShortID != "c8aa6a68a476c885" {
		t.Fatalf("reality options lost: %+v", hk)
	}
	if hk.Flow != "xtls-rprx-vision" || hk.SNI != "github.com" || !hk.TLS {
		t.Fatalf("tls fields lost: %+v", hk)
	}
	if hk.Unsupported != "" {
		t.Fatalf("vless+reality should be usable, got %q", hk.Unsupported)
	}

	jp := findNode(t, result, "日本 02")
	if jp.Network != "ws" || jp.Path != "/ws" || jp.Headers["Host"] != "cdn.example.com" {
		t.Fatalf("ws transport lost: %+v", jp)
	}
	if jp.Cipher != "auto" {
		t.Fatalf("vmess cipher = %q, want auto", jp.Cipher)
	}

	trojan := findNode(t, result, "美国 Trojan")
	if trojan.Password != "trojan-pass" || !trojan.TLS || !trojan.SkipCertVerify {
		t.Fatalf("trojan parsed wrong: %+v", trojan)
	}

	ss := findNode(t, result, "新加坡 SS")
	if ss.Cipher != "chacha20-ietf-poly1305" || ss.Password != "ss-pass" {
		t.Fatalf("ss parsed wrong: %+v", ss)
	}
	if ss.Unsupported != "" {
		t.Fatalf("chacha20 ss should be usable, got %q", ss.Unsupported)
	}

	hy2 := findNode(t, result, "香港 HY2")
	if hy2.Unsupported == "" {
		t.Fatal("hysteria2 must be flagged as unsupported")
	}

	weak := findNode(t, result, "老加密 SS")
	if weak.Unsupported == "" {
		t.Fatal("legacy stream cipher must be flagged as unsupported")
	}

	plug := findNode(t, result, "带插件的 SS")
	if plug.Unsupported == "" {
		t.Fatal("ss with a plugin must be flagged as unsupported")
	}

	if usable := CountUsable(result.Nodes); usable != 4 {
		t.Fatalf("usable = %d, want 4", usable)
	}
}

func findNode(t *testing.T, result Result, name string) node.Node {
	t.Helper()
	for _, n := range result.Nodes {
		if n.Name == name {
			return n
		}
	}
	t.Fatalf("node %q not found", name)
	return node.Node{}
}

func TestParseV2RayLinks(t *testing.T) {
	links := []string{
		"vless://49b4b82b-73f0-4772-86ca-ca5059375c63@hk.example.com:443?encryption=none&security=reality&sni=github.com&fp=firefox&pbk=PUBKEY123&sid=abcd1234&spx=%2F&flow=xtls-rprx-vision&type=tcp#香港01",
		"trojan://secret@us.example.com:443?security=tls&sni=us.example.com&allowInsecure=1#美国01",
		"ss://YWVzLTI1Ni1nY206cGFzc3dvcmQ@sg.example.com:8388#新加坡01",
		"vmess://" + base64.StdEncoding.EncodeToString([]byte(`{"v":"2","ps":"日本01","add":"jp.example.com","port":"443","id":"b831381d-6324-4d53-ad4f-8cda48b30811","aid":"0","scy":"auto","net":"ws","type":"none","host":"cdn.example.com","path":"/ws","tls":"tls","sni":"cdn.example.com"}`)),
	}
	payload := base64.StdEncoding.EncodeToString([]byte(joinLines(links)))

	result, err := Parse([]byte(payload), "auto")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Format != "v2ray" {
		t.Fatalf("format = %q, want v2ray", result.Format)
	}
	if len(result.Nodes) != 4 {
		t.Fatalf("got %d nodes, want 4", len(result.Nodes))
	}

	hk := findNode(t, result, "香港01")
	if hk.UUID != "49b4b82b-73f0-4772-86ca-ca5059375c63" || hk.RealityPublicKey != "PUBKEY123" {
		t.Fatalf("vless link parsed wrong: %+v", hk)
	}
	if hk.RealitySpiderX != "/" || hk.Fingerprint != "firefox" {
		t.Fatalf("spiderX/fp wrong: %+v", hk)
	}

	us := findNode(t, result, "美国01")
	if us.Password != "secret" || !us.SkipCertVerify || !us.TLS {
		t.Fatalf("trojan link parsed wrong: %+v", us)
	}

	sg := findNode(t, result, "新加坡01")
	if sg.Cipher != "aes-256-gcm" || sg.Password != "password" {
		t.Fatalf("ss link parsed wrong: %+v", sg)
	}

	jp := findNode(t, result, "日本01")
	if jp.Type != "vmess" || jp.Network != "ws" || jp.Path != "/ws" || !jp.TLS {
		t.Fatalf("vmess link parsed wrong: %+v", jp)
	}
	if jp.Host != "cdn.example.com" {
		t.Fatalf("vmess host = %q", jp.Host)
	}
}

func TestParsePlainLinksWithoutBase64(t *testing.T) {
	raw := "vless://abc@a.example.com:443?security=tls&sni=a.example.com&type=tcp#节点A\n" +
		"# 这是一行注释\n" +
		"socks5://user:pass@b.example.com:1080#节点B\n"
	result, err := Parse([]byte(raw), "auto")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(result.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(result.Nodes))
	}
	b := findNode(t, result, "节点B")
	if b.Type != "socks" || b.UUID != "user" || b.Password != "pass" {
		t.Fatalf("socks link parsed wrong: %+v", b)
	}
}

func TestParseRejectsGarbage(t *testing.T) {
	if _, err := Parse([]byte("这是一段无关的文字"), "auto"); err == nil {
		t.Fatal("expected an error for unrecognised content")
	}
	if _, err := Parse([]byte(""), "auto"); err == nil {
		t.Fatal("expected an error for empty content")
	}
}

func joinLines(lines []string) string {
	out := ""
	for i, l := range lines {
		if i > 0 {
			out += "\n"
		}
		out += l
	}
	return out
}
