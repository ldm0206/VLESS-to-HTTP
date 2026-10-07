package node

import (
	"fmt"
	"strings"
)

// Outbound renders the node as an Xray outbound document. The caller supplies
// the tag so the router can reference it.
func (n *Node) Outbound(tag string) (map[string]any, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}

	out := map[string]any{
		"tag":      tag,
		"protocol": n.Type,
		"settings": n.settings(),
	}
	if stream := n.streamSettings(); len(stream) > 0 {
		out["streamSettings"] = stream
	}
	return out, nil
}

func (n *Node) settings() map[string]any {
	switch n.Type {
	case "vless":
		user := map[string]any{"id": n.UUID, "encryption": "none", "level": 0}
		// flow=xtls-rprx-vision only exists on raw TCP; subscriptions that
		// carry it alongside ws/grpc would make Xray refuse to start.
		if n.Flow != "" && n.Network == "tcp" {
			user["flow"] = n.Flow
		}
		return map[string]any{
			"vnext": []any{map[string]any{
				"address": n.Server,
				"port":    n.Port,
				"users":   []any{user},
			}},
		}

	case "vmess":
		cipher := n.Cipher
		if cipher == "" || cipher == "auto" {
			cipher = "auto"
		}
		return map[string]any{
			"vnext": []any{map[string]any{
				"address": n.Server,
				"port":    n.Port,
				"users": []any{map[string]any{
					"id":       n.UUID,
					"alterId":  n.AlterID,
					"security": cipher,
					"level":    0,
				}},
			}},
		}

	case "trojan":
		return map[string]any{
			"servers": []any{map[string]any{
				"address":  n.Server,
				"port":     n.Port,
				"password": n.Password,
				"level":    0,
			}},
		}

	case "ss":
		return map[string]any{
			"servers": []any{map[string]any{
				"address":  n.Server,
				"port":     n.Port,
				"method":   n.Cipher,
				"password": n.Password,
				"level":    0,
			}},
		}

	case "socks", "http":
		server := map[string]any{
			"address": n.Server,
			"port":    n.Port,
		}
		if n.Password != "" {
			server["users"] = []any{map[string]any{"user": n.UUID, "pass": n.Password}}
		}
		return map[string]any{"servers": []any{server}}
	}
	return map[string]any{}
}

func (n *Node) streamSettings() map[string]any {
	stream := map[string]any{"network": n.Network}

	switch {
	case n.RealityPublicKey != "" && n.Network != "xhttp":
		// xhttp carries REALITY through its own settings block below.
		fallthrough
	case n.RealityPublicKey != "":
		stream["security"] = "reality"
		stream["realitySettings"] = map[string]any{
			"show":        false,
			"publicKey":   n.RealityPublicKey,
			"shortId":     n.RealityShortID,
			"spiderX":     spiderX(n.RealitySpiderX),
			"fingerprint": orDefault(n.Fingerprint, "chrome"),
			"serverName":  n.SNI,
		}
	case n.TLS || n.Type == "trojan":
		tls := map[string]any{
			"serverName":  n.SNI,
			"fingerprint": orDefault(n.Fingerprint, "chrome"),
		}
		// Xray removed "allowInsecure" and now refuses any config that still
		// carries it, so a subscription asking to skip verification is served
		// by pinning the certificate instead. See internal/certpin.
		if n.PinnedCertSha256 != "" {
			tls["pinnedPeerCertSha256"] = n.PinnedCertSha256
		}
		if len(n.ALPN) > 0 {
			tls["alpn"] = toAnySlice(n.ALPN)
		}
		stream["security"] = "tls"
		stream["tlsSettings"] = tls
	default:
		stream["security"] = "none"
	}

	switch n.Network {
	case "ws":
		ws := map[string]any{"path": orDefault(n.Path, "/")}
		// Xray moved the Host header to its own field; the headers map is now
		// only for extra headers and is deprecated.
		host := n.Host
		headers := map[string]any{}
		for k, v := range n.Headers {
			if strings.EqualFold(k, "Host") {
				if host == "" {
					host = v
				}
				continue
			}
			headers[k] = v
		}
		if host != "" {
			ws["host"] = host
		}
		if len(headers) > 0 {
			ws["headers"] = headers
		}
		stream["wsSettings"] = ws

	case "grpc":
		grpc := map[string]any{"serviceName": n.ServiceName}
		if n.GRPCMultiMode {
			grpc["multiMode"] = true
		}
		stream["grpcSettings"] = grpc

	case "h2":
		http := map[string]any{"path": orDefault(n.Path, "/")}
		if n.Host != "" {
			http["host"] = splitHosts(n.Host)
		}
		stream["httpSettings"] = http

	case "httpupgrade":
		upgrade := map[string]any{"path": orDefault(n.Path, "/")}
		if n.Host != "" {
			upgrade["host"] = n.Host
		}
		stream["httpupgradeSettings"] = upgrade

	case "xhttp":
		xhttp := map[string]any{"path": orDefault(n.Path, "/")}
		if n.Host != "" {
			xhttp["host"] = n.Host
		}
		if n.XHTTPMode != "" {
			xhttp["mode"] = n.XHTTPMode
		}
		stream["xhttpSettings"] = xhttp
	}

	return stream
}

// OutboundTag is the tag a node's outbound carries inside the Xray config.
func OutboundTag(id string) string { return "node:" + id }

// ProbeInboundTag is the tag of the loopback SOCKS inbound used to test a node.
func ProbeInboundTag(id string) string { return "probe:" + id }

func orDefault(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func spiderX(v string) string {
	if v == "" {
		return "/"
	}
	return v
}

func toAnySlice(in []string) []any {
	out := make([]any, 0, len(in))
	for _, v := range in {
		out = append(out, v)
	}
	return out
}

func splitHosts(host string) []any {
	parts := []string{}
	cur := ""
	for _, r := range host {
		if r == ',' {
			if cur != "" {
				parts = append(parts, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		parts = append(parts, cur)
	}
	return toAnySlice(parts)
}

// String renders a short human description used in logs.
func (n *Node) String() string {
	return fmt.Sprintf("%s[%s] %s:%d", n.Label(), n.Type, n.Server, n.Port)
}
