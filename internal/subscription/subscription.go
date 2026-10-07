// Package subscription fetches and parses remote node lists, in both the
// Clash YAML and the base64-link formats.
package subscription

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/node"
)

// Result is a parsed subscription payload.
type Result struct {
	Nodes  []node.Node
	Format string // clash | v2ray
}

// Parse decodes a payload. kind is auto, clash or v2ray.
func Parse(raw []byte, kind string) (Result, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return Result{}, fmt.Errorf("订阅内容为空")
	}

	switch strings.ToLower(kind) {
	case "clash":
		nodes, err := parseClash(trimmed)
		return Result{Nodes: nodes, Format: "clash"}, err
	case "v2ray":
		nodes, err := parseV2Ray(trimmed)
		return Result{Nodes: nodes, Format: "v2ray"}, err
	}

	// auto: most providers hand out Clash YAML, a base64 blob of node links,
	// or a base64-wrapped YAML. Try the payload as-is, then decoded.
	candidates := [][]byte{trimmed}
	if decoded, ok := decodeBase64(string(trimmed)); ok {
		candidates = append(candidates, []byte(decoded))
	}

	for _, candidate := range candidates {
		if looksLikeClash(candidate) {
			if nodes, err := parseClash(candidate); err == nil {
				return Result{Nodes: nodes, Format: "clash"}, nil
			}
		}
		if bytes.Contains(candidate, []byte("://")) {
			if nodes, err := parseV2Ray(candidate); err == nil {
				return Result{Nodes: nodes, Format: "v2ray"}, nil
			}
		}
	}
	return Result{}, fmt.Errorf("无法识别的订阅格式：既不是 Clash YAML，也不是节点链接列表")
}

// looksLikeClash reports whether the payload carries a Clash proxies section.
func looksLikeClash(raw []byte) bool {
	head := raw
	if len(head) > 4096 {
		head = head[:4096]
	}
	text := string(head)
	return strings.Contains(text, "proxies:") || strings.Contains(text, "\"proxies\"")
}

// CountUsable returns how many nodes can actually be dialed.
func CountUsable(nodes []node.Node) int {
	n := 0
	for i := range nodes {
		if nodes[i].Unsupported == "" {
			n++
		}
	}
	return n
}
