// Package node models a single proxy server parsed out of a subscription and
// knows how to turn it into an Xray outbound.
package node

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strings"
)

// Node is one proxy server. Every field is optional: which ones matter
// depends on Type, and Unsupported explains why a node cannot be used at all.
type Node struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // vless | vmess | trojan | ss | socks | http
	// SubID records which subscription the node came from.
	SubID  string `json:"sub_id"`
	Server string `json:"server"`
	Port   int    `json:"port"`

	UUID     string `json:"uuid,omitempty"`
	AlterID  int    `json:"alter_id,omitempty"`
	Flow     string `json:"flow,omitempty"`
	Cipher   string `json:"cipher,omitempty"`
	Password string `json:"password,omitempty"`

	TLS            bool     `json:"tls,omitempty"`
	SNI            string   `json:"sni,omitempty"`
	ALPN           []string `json:"alpn,omitempty"`
	SkipCertVerify bool     `json:"skip_cert_verify,omitempty"`
	Fingerprint    string   `json:"fingerprint,omitempty"`

	RealityPublicKey string `json:"reality_public_key,omitempty"`
	RealityShortID   string `json:"reality_short_id,omitempty"`
	RealitySpiderX   string `json:"reality_spider_x,omitempty"`

	Network       string            `json:"network,omitempty"` // tcp | ws | grpc | h2 | httpupgrade | xhttp
	Path          string            `json:"path,omitempty"`
	Host          string            `json:"host,omitempty"`
	ServiceName   string            `json:"service_name,omitempty"`
	Headers       map[string]string `json:"headers,omitempty"`
	XHTTPMode     string            `json:"xhttp_mode,omitempty"`
	GRPCMultiMode bool              `json:"grpc_multi_mode,omitempty"`

	// Plugin is Clash's shadowsocks plugin name; Xray has no equivalent.
	Plugin string `json:"plugin,omitempty"`

	// UDP reports whether the server side carries UDP.
	UDP bool `json:"udp,omitempty"`

	// Unsupported carries a human-readable reason when the node cannot be
	// turned into an Xray outbound (e.g. hysteria2, ss with a plugin).
	Unsupported string `json:"unsupported,omitempty"`
}

// supportedTypes lists the protocols Xray can dial as an outbound.
var supportedTypes = map[string]bool{
	"vless":  true,
	"vmess":  true,
	"trojan": true,
	"ss":     true,
	"socks":  true,
	"http":   true,
}

// SupportedTypes returns the protocol names the panel accepts.
func SupportedTypes() []string {
	return []string{"vless", "vmess", "trojan", "ss", "socks", "http"}
}

// supportedCiphers are the Shadowsocks methods Xray implements. Clash configs
// often carry legacy stream ciphers that would silently fail here.
var supportedCiphers = map[string]bool{
	"aes-128-gcm":                   true,
	"aes-256-gcm":                   true,
	"chacha20-ietf-poly1305":        true,
	"xchacha20-ietf-poly1305":       true,
	"none":                          true,
	"2022-blake3-aes-128-gcm":       true,
	"2022-blake3-aes-256-gcm":       true,
	"2022-blake3-chacha20-poly1305": true,
}

// supportedNetworks are the transports mapped onto Xray stream settings.
var supportedNetworks = map[string]bool{
	"":            true,
	"tcp":         true,
	"ws":          true,
	"grpc":        true,
	"h2":          true,
	"http":        true,
	"httpupgrade": true,
	"xhttp":       true,
}

// Normalize fills in defaults and computes the stable ID.
func (n *Node) Normalize() {
	n.Type = strings.ToLower(strings.TrimSpace(n.Type))
	n.Name = strings.TrimSpace(n.Name)
	n.Server = strings.TrimSpace(n.Server)
	n.Network = strings.ToLower(strings.TrimSpace(n.Network))
	if n.Network == "http" {
		n.Network = "h2"
	}
	if n.Fingerprint == "" {
		n.Fingerprint = "chrome"
	}
	if n.Network == "" {
		n.Network = "tcp"
	}
	if n.Headers == nil {
		n.Headers = map[string]string{}
	}
	if n.Type == "ss" {
		if n.Cipher == "" {
			n.Cipher = "none"
		}
		n.UDP = true
	}
	n.Unsupported = n.unsupportedReason()
	// Always derived from the connection parameters, never carried over from a
	// previous parse, unless a caller pinned one explicitly.
	n.ID = computeID(n)
}

// Validate reports whether the node has everything needed to build an outbound.
func (n *Node) Validate() error {
	if n.Unsupported != "" {
		return fmt.Errorf("%s", n.Unsupported)
	}
	if n.Server == "" {
		return fmt.Errorf("节点 %q 缺少服务器地址", n.Name)
	}
	if n.Port <= 0 || n.Port > 65535 {
		return fmt.Errorf("节点 %q 的端口非法：%d", n.Name, n.Port)
	}
	switch n.Type {
	case "vless", "vmess":
		if n.UUID == "" {
			return fmt.Errorf("节点 %q 缺少 UUID", n.Name)
		}
	case "trojan", "ss", "socks", "http":
		if n.Password == "" {
			return fmt.Errorf("节点 %q 缺少密码", n.Name)
		}
	default:
		return fmt.Errorf("节点 %q 的协议 %q 不受支持", n.Name, n.Type)
	}
	return nil
}

func (n *Node) unsupportedReason() string {
	if n.Type == "" {
		return "节点缺少协议类型"
	}
	if !supportedTypes[n.Type] {
		switch n.Type {
		case "hysteria", "hysteria2", "tuic", "anytls", "ssh", "wireguard", "snell", "mieru":
			return fmt.Sprintf("Xray 内核不支持 %s 协议", n.Type)
		case "direct":
			return "这是直连策略组，不是真实节点"
		default:
			return fmt.Sprintf("未知协议 %q", n.Type)
		}
	}
	if !supportedNetworks[n.Network] {
		return fmt.Sprintf("Xray 内核不支持 %s 传输层", n.Network)
	}
	if n.Type == "ss" && !supportedCiphers[n.Cipher] {
		return fmt.Sprintf("Xray 内核不支持 %s 加密方式", n.Cipher)
	}
	if n.Type == "ss" && n.Plugin != "" {
		// Clash's ss plugin (obfs / v2ray-plugin) has no Xray equivalent.
		return "Xray 内核不支持 shadowsocks 插件（obfs/v2ray-plugin）"
	}
	return ""
}

// computeID hashes the connection parameters so the same server keeps its ID
// across subscription updates even if the display name changes.
func computeID(n *Node) string {
	secret := n.UUID
	if secret == "" {
		secret = n.Password
	}
	h := sha1.Sum([]byte(strings.Join([]string{
		n.Type, n.Server, fmt.Sprint(n.Port), secret,
		n.Network, n.Path, n.SNI, n.Cipher,
	}, "|")))
	return hex.EncodeToString(h[:])[:12]
}

// Label is the display form used in logs and the panel.
func (n *Node) Label() string {
	if n.Name != "" {
		return n.Name
	}
	return fmt.Sprintf("%s:%d", n.Server, n.Port)
}
