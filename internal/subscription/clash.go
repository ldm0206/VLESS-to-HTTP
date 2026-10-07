package subscription

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/ldm0206/vless-to-http/internal/node"
)

type clashDoc struct {
	Proxies []map[string]any `yaml:"proxies"`
}

// parseClash turns a Clash/clash-meta YAML document into nodes. Entries the
// Xray core cannot dial are kept, flagged with a reason, so the panel can show
// the user what was skipped instead of silently dropping servers.
func parseClash(raw []byte) ([]node.Node, error) {
	var doc clashDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析 Clash YAML 失败：%w", err)
	}
	if len(doc.Proxies) == 0 {
		return nil, fmt.Errorf("Clash 配置里没有 proxies 列表")
	}

	out := make([]node.Node, 0, len(doc.Proxies))
	for i, p := range doc.Proxies {
		n := clashProxy(p)
		if n.Server == "" && n.Name == "" {
			continue
		}
		if n.Name == "" {
			n.Name = fmt.Sprintf("%s-%d", n.Server, i+1)
		}
		n.Normalize()
		out = append(out, n)
	}
	return out, nil
}

func clashProxy(p map[string]any) node.Node {
	// Clash spells the protocol "type"; some dialects use "network" for the
	// transport, which we read separately.
	n := node.Node{
		Name:      str(p, "name"),
		Type:      strings.ToLower(str(p, "type")),
		Server:    str(p, "server"),
		Port:      integer(p, "port"),
		UUID:      str(p, "uuid"),
		AlterID:   integer(p, "alterId"),
		Flow:      str(p, "flow"),
		Cipher:    str(p, "cipher"),
		Password:  str(p, "password"),
		Plugin:    str(p, "plugin"),
		Network:   strings.ToLower(str(p, "network")),
		SkipCertVerify: boolean(p, "skip-cert-verify"),
		Fingerprint:    str(p, "client-fingerprint"),
		ALPN:           strSlice(p, "alpn"),
	}

	switch n.Type {
	case "vless", "vmess":
		n.TLS = boolean(p, "tls")
		n.SNI = firstNonEmpty(str(p, "servername"), str(p, "sni"))
	case "trojan":
		// Trojan is TLS by definition unless the provider disables it.
		n.TLS = true
		if v, ok := p["tls"]; ok {
			n.TLS = truthy(v)
		}
		n.SNI = firstNonEmpty(str(p, "sni"), str(p, "servername"))
	case "ss":
		n.TLS = boolean(p, "tls")
		n.SNI = str(p, "sni")
	case "socks5", "socks", "http":
		n.Type = strings.TrimSuffix(n.Type, "5")
		n.TLS = boolean(p, "tls")
		n.SNI = str(p, "sni")
		if n.Password == "" {
			n.Password = str(p, "password")
		}
		n.UUID = firstNonEmpty(str(p, "username"), str(p, "uuid"))
	}

	// REALITY
	if opts := sub(p, "reality-opts"); opts != nil {
		n.RealityPublicKey = str(opts, "public-key")
		n.RealityShortID = str(opts, "short-id")
		n.RealitySpiderX = str(opts, "spider-x")
	}
	if n.RealityPublicKey == "" {
		// Some providers flatten the keys.
		n.RealityPublicKey = str(p, "public-key")
		n.RealityShortID = firstNonEmpty(str(p, "short-id"), str(p, "shortId"))
	}

	switch n.Network {
	case "ws":
		if opts := sub(p, "ws-opts"); opts != nil {
			n.Path = str(opts, "path")
			if headers := sub(opts, "headers"); headers != nil {
				n.Headers = map[string]string{}
				for k, v := range headers {
					n.Headers[k] = fmt.Sprint(v)
				}
				n.Host = n.Headers["Host"]
			}
		}
		if n.Path == "" {
			n.Path = str(p, "ws-path")
		}
		if n.Host == "" {
			n.Host = str(p, "ws-headers.Host")
		}
	case "grpc":
		if opts := sub(p, "grpc-opts"); opts != nil {
			n.ServiceName = str(opts, "grpc-service-name")
		}
	case "h2", "http":
		if opts := sub(p, "h2-opts"); opts != nil {
			n.Path = str(opts, "path")
			if hosts := strSlice(opts, "host"); len(hosts) > 0 {
				n.Host = strings.Join(hosts, ",")
			}
		}
	case "httpupgrade":
		if opts := sub(p, "http-upgrade-opts"); opts != nil {
			n.Path = str(opts, "path")
			n.Host = str(opts, "host")
		}
	case "xhttp":
		if opts := sub(p, "xhttp-opts"); opts != nil {
			n.Path = str(opts, "path")
			n.Host = str(opts, "host")
			n.XHTTPMode = str(opts, "mode")
		}
	}

	// VMess defaults: Clash writes alterId 0 for AEAD.
	if n.Type == "vmess" && n.AlterID == 0 {
		n.AlterID = 0
	}
	return n
}

// --- small YAML value helpers -------------------------------------------

func str(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return strings.TrimSpace(t)
	case int:
		return fmt.Sprint(t)
	case int64:
		return fmt.Sprint(t)
	case float64:
		return strings.TrimSuffix(fmt.Sprint(t), ".0")
	case bool:
		if t {
			return "true"
		}
		return "false"
	default:
		return ""
	}
}

func integer(m map[string]any, key string) int {
	if m == nil {
		return 0
	}
	switch t := m[key].(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		var out int
		fmt.Sscanf(strings.TrimSpace(t), "%d", &out)
		return out
	default:
		return 0
	}
}

func boolean(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	return truthy(m[key])
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		s := strings.ToLower(strings.TrimSpace(t))
		return s == "true" || s == "1" || s == "yes"
	case int:
		return t != 0
	case float64:
		return t != 0
	default:
		return false
	}
}

func sub(m map[string]any, key string) map[string]any {
	if m == nil {
		return nil
	}
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return nil
}

func strSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	switch t := m[key].(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			out = append(out, fmt.Sprint(v))
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		parts := strings.Split(t, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	default:
		return nil
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
