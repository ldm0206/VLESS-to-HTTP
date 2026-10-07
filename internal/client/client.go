// Package client is the HTTP client the CLI and the TUI use to talk to a
// running v2h instance.
package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/logs"
)

// Client talks to the panel API with a bearer token.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// New builds a client. base may be "127.0.0.1:9080" or a full URL.
func New(base, token string) *Client {
	if base == "" {
		base = "127.0.0.1:9080"
	}
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	return &Client{
		base:  strings.TrimRight(base, "/"),
		token: token,
		http:  &http.Client{Timeout: 120 * time.Second},
	}
}

// Base returns the API root URL.
func (c *Client) Base() string { return c.base }

// StatusResponse mirrors the panel's status payload.
type StatusResponse struct {
	Status  engine.Status  `json:"status"`
	Version map[string]any `json:"version"`
	Proxy   map[string]any `json:"proxy"`
	Panel   map[string]any `json:"panel"`
	Health  map[string]any `json:"health"`
	Logs    map[string]any `json:"logs"`
}

// APIError carries the message the server returned.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// IsUnauthorized reports whether the token or session was rejected.
func IsUnauthorized(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusUnauthorized
}

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("连接 %s 失败：%w", c.base, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		msg := strings.TrimSpace(string(raw))
		var parsed struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.Error != "" {
			msg = parsed.Error
		}
		return &APIError{Status: resp.StatusCode, Message: msg}
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// --- endpoints -----------------------------------------------------------

// Health checks that the service is reachable.
func (c *Client) Health(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, "/api/health", nil, &out)
	return out, err
}

// Status fetches the full status snapshot.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	var out StatusResponse
	if err := c.do(ctx, http.MethodGet, "/api/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Users lists accounts.
func (c *Client) Users(ctx context.Context) ([]engine.UserStatus, error) {
	var out struct {
		Users []engine.UserStatus `json:"users"`
	}
	err := c.do(ctx, http.MethodGet, "/api/users", nil, &out)
	return out.Users, err
}

// User fetches one account.
func (c *Client) User(ctx context.Context, id string) (*engine.UserStatus, error) {
	var out struct {
		User engine.UserStatus `json:"user"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/users/"+id, nil, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}

// CreateUser adds an account.
func (c *Client) CreateUser(ctx context.Context, body map[string]any) (*engine.UserStatus, error) {
	var out struct {
		User engine.UserStatus `json:"user"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/users", body, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}

// UpdateUser patches an account.
func (c *Client) UpdateUser(ctx context.Context, id string, body map[string]any) (*engine.UserStatus, error) {
	var out struct {
		User engine.UserStatus `json:"user"`
	}
	if err := c.do(ctx, http.MethodPatch, "/api/users/"+id, body, &out); err != nil {
		return nil, err
	}
	return &out.User, nil
}

// DeleteUser removes an account.
func (c *Client) DeleteUser(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/users/"+id, nil, nil)
}

// ResetUserTraffic zeroes one account's counters.
func (c *Client) ResetUserTraffic(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/api/users/"+id+"/traffic/reset", nil, nil)
}

// Subs lists subscriptions.
func (c *Client) Subs(ctx context.Context) ([]engine.SubStatus, error) {
	var out struct {
		Subs []engine.SubStatus `json:"subs"`
	}
	err := c.do(ctx, http.MethodGet, "/api/subs", nil, &out)
	return out.Subs, err
}

// CreateSub adds a subscription and starts its first fetch.
func (c *Client) CreateSub(ctx context.Context, body map[string]any) (*engine.SubStatus, error) {
	var out struct {
		Sub engine.SubStatus `json:"sub"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/subs", body, &out); err != nil {
		return nil, err
	}
	return &out.Sub, nil
}

// UpdateSub patches a subscription.
func (c *Client) UpdateSub(ctx context.Context, id string, body map[string]any) (*engine.SubStatus, error) {
	var out struct {
		Sub engine.SubStatus `json:"sub"`
	}
	if err := c.do(ctx, http.MethodPatch, "/api/subs/"+id, body, &out); err != nil {
		return nil, err
	}
	return &out.Sub, nil
}

// DeleteSub removes a subscription and its cached nodes.
func (c *Client) DeleteSub(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/api/subs/"+id, nil, nil)
}

// RefreshSub re-fetches one subscription.
func (c *Client) RefreshSub(ctx context.Context, id string) (*engine.SubStatus, error) {
	var out struct {
		Sub   engine.SubStatus `json:"sub"`
		Error string           `json:"error"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/subs/"+id+"/refresh", nil, &out); err != nil {
		return nil, err
	}
	if out.Error != "" {
		return &out.Sub, errors.New(out.Error)
	}
	return &out.Sub, nil
}

// ImportSub stores a pasted node list without any network access.
func (c *Client) ImportSub(ctx context.Context, name, kind, content string) (*engine.SubStatus, error) {
	var out struct {
		Sub engine.SubStatus `json:"sub"`
	}
	body := map[string]any{"name": name, "kind": kind, "content": content}
	if err := c.do(ctx, http.MethodPost, "/api/subs/import", body, &out); err != nil {
		return nil, err
	}
	return &out.Sub, nil
}

// Nodes lists nodes, optionally limited to one subscription.
func (c *Client) Nodes(ctx context.Context, subID string) ([]engine.NodeStatus, error) {
	path := "/api/nodes"
	if subID != "" {
		path += "?sub=" + subID
	}
	var out struct {
		Nodes []engine.NodeStatus `json:"nodes"`
	}
	err := c.do(ctx, http.MethodGet, path, nil, &out)
	return out.Nodes, err
}

// TestNodes runs a latency test.
func (c *Client) TestNodes(ctx context.Context, ids []string) (map[string]health.State, error) {
	var out struct {
		Results map[string]health.State `json:"results"`
	}
	body := map[string]any{"ids": ids}
	if err := c.do(ctx, http.MethodPost, "/api/nodes/test", body, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// TestSub runs a latency test for every node of a subscription.
func (c *Client) TestSub(ctx context.Context, subID string) (map[string]health.State, error) {
	var out struct {
		Results map[string]health.State `json:"results"`
	}
	body := map[string]any{"sub": subID}
	if err := c.do(ctx, http.MethodPost, "/api/nodes/test", body, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// Logs fetches recent log entries.
func (c *Client) Logs(ctx context.Context, since uint64, limit int, level, user, query string) ([]logs.Entry, uint64, error) {
	path := fmt.Sprintf("/api/logs?since=%d&limit=%d", since, limit)
	if level != "" {
		path += "&level=" + level
	}
	if user != "" {
		path += "&user=" + user
	}
	if query != "" {
		path += "&q=" + urlEscape(query)
	}
	var out struct {
		Entries []logs.Entry `json:"entries"`
		LastSeq uint64       `json:"last_seq"`
	}
	if err := c.do(ctx, http.MethodGet, path, nil, &out); err != nil {
		return nil, since, err
	}
	return out.Entries, out.LastSeq, nil
}

// Settings reads the panel configuration.
func (c *Client) Settings(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	err := c.do(ctx, http.MethodGet, "/api/settings", nil, &out)
	return out, err
}

// PatchSettings updates part of the panel configuration.
func (c *Client) PatchSettings(ctx context.Context, body map[string]any) error {
	return c.do(ctx, http.MethodPatch, "/api/settings", body, nil)
}

// SetAdminPassword changes the panel password.
func (c *Client) SetAdminPassword(ctx context.Context, current, next string) error {
	return c.do(ctx, http.MethodPost, "/api/settings/password",
		map[string]any{"current": current, "password": next}, nil)
}

// RotateToken issues a new API token.
func (c *Client) RotateToken(ctx context.Context) (string, error) {
	var out struct {
		Token string `json:"api_token"`
	}
	if err := c.do(ctx, http.MethodPost, "/api/settings/token", nil, &out); err != nil {
		return "", err
	}
	return out.Token, nil
}

// RestartCore restarts the proxy core.
func (c *Client) RestartCore(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/api/core/restart", nil, nil)
}

// CoreConfig returns the generated Xray configuration.
func (c *Client) CoreConfig(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/core/config", nil)
	if err != nil {
		return "", err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode >= 400 {
		return "", &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}
	return string(raw), nil
}

// Event is one server-sent event.
type Event struct {
	Name string
	Data []byte
}

// Watch opens the event stream. The returned channel closes when the stream
// ends; cancelling ctx stops it.
func (c *Client) Watch(ctx context.Context) (<-chan Event, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/api/events", nil)
	if err != nil {
		return nil, err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "text/event-stream")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		resp.Body.Close()
		return nil, &APIError{Status: resp.StatusCode, Message: "事件流不可用"}
	}

	out := make(chan Event, 64)
	go func() {
		defer resp.Body.Close()
		defer close(out)

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

		var name string
		var data []string
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case line == "":
				if len(data) > 0 {
					select {
					case out <- Event{Name: name, Data: []byte(strings.Join(data, "\n"))}:
					case <-ctx.Done():
						return
					}
				}
				name, data = "", nil
			case strings.HasPrefix(line, "event:"):
				name = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			case strings.HasPrefix(line, "data:"):
				data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
			}
		}
	}()
	return out, nil
}

// UserStatusSummary is a short human description used by the CLI.
func UserStatusSummary(u engine.UserStatus) string {
	if !u.Enabled {
		return "已停用"
	}
	switch {
	case u.Balancer:
		return fmt.Sprintf("自动切换（%d 个节点）", u.NodeCount)
	case u.ActiveNode != "":
		return "使用 " + u.ActiveNode
	case len(u.Targets) == 0:
		return "未设置目标（走兜底）"
	default:
		return "无可用节点（走兜底）"
	}
}

// FormatBytes renders a byte count for humans.
func FormatBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value := float64(n)
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	for _, u := range units {
		value /= unit
		if value < unit {
			return fmt.Sprintf("%.2f %s", value, u)
		}
	}
	return fmt.Sprintf("%.2f EB", value/unit)
}

// SubModeLabel describes a subscription's format setting.
func SubModeLabel(kind string) string {
	switch kind {
	case "clash":
		return "Clash"
	case "v2ray":
		return "v2ray"
	default:
		return "自动识别"
	}
}

// TargetsToBody converts targets for API calls.
func TargetsToBody(targets []config.Target) []map[string]any {
	out := make([]map[string]any, 0, len(targets))
	for _, t := range targets {
		item := map[string]any{"sub": t.Sub, "node": t.Node, "all": t.All}
		if t.Limit > 0 {
			item["limit"] = t.Limit
		}
		out = append(out, item)
	}
	return out
}

func urlEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.', r == '~':
			b.WriteRune(r)
		case r == ' ':
			b.WriteString("+")
		default:
			for _, by := range []byte(string(r)) {
				fmt.Fprintf(&b, "%%%02X", by)
			}
		}
	}
	return b.String()
}
