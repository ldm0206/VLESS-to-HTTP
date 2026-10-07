package subscription

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/node"
)

// parseV2Ray handles the base64-of-links format most panels hand out. It also
// accepts a plain list of links pasted by hand.
func parseV2Ray(raw []byte) ([]node.Node, error) {
	text := string(raw)
	if !strings.Contains(text, "://") {
		decoded, ok := decodeBase64(text)
		if !ok {
			return nil, fmt.Errorf("内容既不是 Clash 配置，也不是 base64 订阅")
		}
		text = decoded
	}

	var out []node.Node
	var skipped int
	for _, line := range strings.FieldsFunc(text, func(r rune) bool { return r == '\n' || r == '\r' }) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		n, err := parseLink(line)
		if err != nil {
			skipped++
			continue
		}
		n.Normalize()
		out = append(out, *n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有解析出任何节点（跳过 %d 行）", skipped)
	}
	return out, nil
}

func parseLink(link string) (*node.Node, error) {
	scheme, rest, ok := strings.Cut(link, "://")
	if !ok {
		return nil, fmt.Errorf("缺少协议前缀")
	}
	scheme = strings.ToLower(scheme)

	switch scheme {
	case "vless", "trojan":
		return parseStandardURI(scheme, rest)
	case "vmess":
		return parseVMessURI(rest)
	case "ss":
		return parseShadowsocksURI(rest)
	case "socks", "socks5", "http", "https":
		return parseSocksLikeURI(scheme, rest)
	default:
		// A protocol the core cannot dial is kept as a labelled node rather
		// than dropped, so the panel can tell the operator what was skipped.
		if n := parseUnsupportedURI(scheme, rest); n != nil {
			return n, nil
		}
		return nil, fmt.Errorf("不支持的链接协议 %q", scheme)
	}
}

// parseUnsupportedURI records name, host and port of a link whose protocol we
// cannot use, leaving Normalize to explain why.
func parseUnsupportedURI(scheme, rest string) *node.Node {
	body, name := splitFragment(rest)
	endpoint, _ := splitQuery(body)

	host, port := "", 0
	if at := strings.LastIndex(endpoint, "@"); at >= 0 {
		host, port = hostPort(endpoint[at+1:])
	} else {
		host, port = hostPort(endpoint)
	}
	if host == "" {
		return nil
	}

	n := &node.Node{
		Name:   name,
		Type:   scheme,
		Server: host,
		Port:   port,
	}
	if n.Name == "" {
		n.Name = fmt.Sprintf("%s:%d", host, port)
	}
	return n
}

// splitFragment separates "#name" before the URL parser can choke on it.
func splitFragment(raw string) (body, name string) {
	if i := strings.LastIndex(raw, "#"); i >= 0 {
		return raw[:i], decoded(raw[i+1:])
	}
	return raw, ""
}

// splitQuery separates "?query" from the endpoint part.
func splitQuery(body string) (endpoint, query string) {
	if i := strings.Index(body, "?"); i >= 0 {
		return body[:i], body[i+1:]
	}
	return body, ""
}

// hostPort parses "host:port", tolerating bracketed IPv6.
func hostPort(s string) (string, int) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", 0
	}
	if strings.HasPrefix(s, "[") {
		if end := strings.Index(s, "]"); end > 0 {
			host := s[1:end]
			port := 0
			if rest := strings.TrimPrefix(s[end+1:], ":"); rest != "" {
				port, _ = strconv.Atoi(rest)
			}
			return host, port
		}
	}
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return s, 0
	}
	port, _ := strconv.Atoi(strings.TrimSpace(s[i+1:]))
	return s[:i], port
}

func parseStandardURI(scheme, rest string) (*node.Node, error) {
	body, name := splitFragment(rest)
	endpoint, query := splitQuery(body)

	// credentials@host:port — the credential may itself contain '@'.
	at := strings.LastIndex(endpoint, "@")
	if at < 0 {
		return nil, fmt.Errorf("%s 链接缺少用户信息", scheme)
	}
	cred := decoded(endpoint[:at])
	host, port := hostPort(endpoint[at+1:])
	if host == "" || port == 0 {
		return nil, fmt.Errorf("%s 链接缺少有效的服务器地址", scheme)
	}

	params := parseQuery(query)
	n := &node.Node{
		Name:     name,
		Type:     scheme,
		Server:   host,
		Port:     port,
		Network:  firstNonEmpty(params.Get("type"), "tcp"),
		Path:     decoded(firstNonEmpty(params.Get("path"), "")),
		Host:     firstNonEmpty(params.Get("host"), params.Get("sni")),
		SNI:      firstNonEmpty(params.Get("sni"), params.Get("peer"), params.Get("host")),
		Flow:     params.Get("flow"),
		Fingerprint: firstNonEmpty(params.Get("fp"), params.Get("fingerprint")),
		ALPN:     splitCSV(params.Get("alpn")),
		ServiceName: params.Get("serviceName"),
		XHTTPMode:   params.Get("mode"),
		SkipCertVerify: truthy(params.Get("allowInsecure")) || truthy(params.Get("insecure")),
	}

	switch scheme {
	case "vless":
		n.UUID = cred
		security := strings.ToLower(params.Get("security"))
		if pk := params.Get("pbk"); pk != "" {
			n.RealityPublicKey = pk
			n.RealityShortID = params.Get("sid")
			n.RealitySpiderX = decoded(params.Get("spx"))
			n.TLS = true
		} else if security == "tls" || security == "xtls" || params.Get("tls") == "1" {
			n.TLS = true
		}
	case "trojan":
		n.Password = cred
		n.TLS = true
		if params.Get("security") == "none" {
			n.TLS = false
		}
	}

	return n, nil
}

type vmessJSON struct {
	PS   string `json:"ps"`
	Add  string `json:"add"`
	Port any    `json:"port"`
	ID   string `json:"id"`
	Aid  any    `json:"aid"`
	Scy  string `json:"scy"`
	Net  string `json:"net"`
	Type string `json:"type"`
	Host string `json:"host"`
	Path string `json:"path"`
	TLS  string `json:"tls"`
	SNI  string `json:"sni"`
	ALPN string `json:"alpn"`
	FP   string `json:"fp"`
}

func parseVMessURI(rest string) (*node.Node, error) {
	raw, ok := decodeBase64(rest)
	if !ok {
		return nil, fmt.Errorf("vmess 链接不是有效的 base64")
	}
	var v vmessJSON
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, fmt.Errorf("解析 vmess JSON 失败：%w", err)
	}

	port := 0
	switch t := v.Port.(type) {
	case float64:
		port = int(t)
	case string:
		port, _ = strconv.Atoi(strings.TrimSpace(t))
	}
	aid := 0
	switch t := v.Aid.(type) {
	case float64:
		aid = int(t)
	case string:
		aid, _ = strconv.Atoi(strings.TrimSpace(t))
	}
	if v.Add == "" || port == 0 || v.ID == "" {
		return nil, fmt.Errorf("vmess 链接缺少必要字段")
	}

	n := &node.Node{
		Name:        v.PS,
		Type:        "vmess",
		Server:      v.Add,
		Port:        port,
		UUID:        v.ID,
		AlterID:     aid,
		Cipher:      firstNonEmpty(v.Scy, "auto"),
		Network:     firstNonEmpty(v.Net, "tcp"),
		Path:        v.Path,
		Host:        v.Host,
		SNI:         firstNonEmpty(v.SNI, v.Host),
		Fingerprint: v.FP,
		ALPN:        splitCSV(v.ALPN),
		TLS:         strings.EqualFold(v.TLS, "tls"),
	}
	if n.Network == "h2" && n.Path == "" {
		n.Path = "/"
	}
	if n.Name == "" {
		n.Name = fmt.Sprintf("%s:%d", n.Server, n.Port)
	}
	return n, nil
}

func parseShadowsocksURI(rest string) (*node.Node, error) {
	body, name := splitFragment(rest)
	endpoint, query := splitQuery(body)
	params := parseQuery(query)

	n := &node.Node{Name: name, Type: "ss", Plugin: params.Get("plugin")}

	at := strings.LastIndex(endpoint, "@")
	if at < 0 {
		// Legacy ss://base64(method:password@host:port)
		decodedRaw, ok := decodeBase64(endpoint)
		if !ok {
			return nil, fmt.Errorf("ss 链接格式无法识别")
		}
		at = strings.LastIndex(decodedRaw, "@")
		if at < 0 {
			return nil, fmt.Errorf("ss 链接缺少服务器地址")
		}
		endpoint = decodedRaw
	}

	credPart := endpoint[:at]
	host, port := hostPort(endpoint[at+1:])
	if host == "" || port == 0 {
		return nil, fmt.Errorf("ss 链接缺少有效的服务器地址")
	}

	// SIP002 keeps the method:password pair base64 encoded.
	if decodedCred, ok := decodeBase64(credPart); ok && strings.Contains(decodedCred, ":") {
		credPart = decodedCred
	} else {
		credPart = decoded(credPart)
	}
	method, password, ok := strings.Cut(credPart, ":")
	if !ok {
		return nil, fmt.Errorf("ss 链接缺少加密方式或密码")
	}

	n.Server = host
	n.Port = port
	n.Cipher = strings.ToLower(strings.TrimSpace(method))
	n.Password = password
	if plugin := params.Get("plugin"); plugin != "" {
		n.Plugin = plugin
	}
	return n, nil
}

func parseSocksLikeURI(scheme, rest string) (*node.Node, error) {
	body, name := splitFragment(rest)
	endpoint, query := splitQuery(body)
	params := parseQuery(query)

	proto := "socks"
	if strings.HasPrefix(scheme, "http") {
		proto = "http"
	}

	at := strings.LastIndex(endpoint, "@")
	if at < 0 {
		return nil, fmt.Errorf("%s 链接缺少认证信息", scheme)
	}
	cred := endpoint[:at]
	host, port := hostPort(endpoint[at+1:])
	if host == "" || port == 0 {
		return nil, fmt.Errorf("%s 链接缺少有效的服务器地址", scheme)
	}
	if decodedCred, ok := decodeBase64(cred); ok && strings.Contains(decodedCred, ":") {
		cred = decodedCred
	}
	user, pass, _ := strings.Cut(cred, ":")

	return &node.Node{
		Name:     name,
		Type:     proto,
		Server:   host,
		Port:     port,
		UUID:     user,
		Password: pass,
		TLS:      scheme == "https" || truthy(params.Get("tls")),
		SNI:      params.Get("sni"),
	}, nil
}

func parseQuery(q string) url.Values {
	values, err := url.ParseQuery(q)
	if err != nil {
		// Tolerate sloppy providers: drop the offending pairs and retry.
		clean := strings.ReplaceAll(q, " ", "%20")
		if values, err = url.ParseQuery(clean); err != nil {
			return url.Values{}
		}
	}
	return values
}

func decoded(s string) string {
	out, err := url.QueryUnescape(s)
	if err != nil {
		return s
	}
	return out
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// decodeBase64 accepts standard and URL-safe alphabets, with or without
// padding, which is everything panels emit in practice.
func decodeBase64(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding, base64.RawStdEncoding,
		base64.URLEncoding, base64.RawURLEncoding,
	}
	for _, enc := range encodings {
		if out, err := enc.DecodeString(s); err == nil {
			return string(out), true
		}
	}
	return "", false
}
