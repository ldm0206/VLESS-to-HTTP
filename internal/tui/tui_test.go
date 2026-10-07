package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/node"
)

// --- fake client ----------------------------------------------------------

type userPatch struct {
	id   string
	body map[string]any
}

// fakeClient serves canned data so the model renders without a server.
type fakeClient struct {
	status  *client.StatusResponse
	users   []engine.UserStatus
	subs    []engine.SubStatus
	nodes   []engine.NodeStatus
	entries []logs.Entry
	err     error

	patches   []userPatch
	resets    []string
	refreshes []string
	restarts  int
	tests     []string
}

var _ Client = (*fakeClient)(nil)

func (f *fakeClient) Base() string { return "http://127.0.0.1:9080" }

func (f *fakeClient) Status(context.Context) (*client.StatusResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.status, nil
}

func (f *fakeClient) Users(context.Context) ([]engine.UserStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.users, nil
}

func (f *fakeClient) Subs(context.Context) ([]engine.SubStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.subs, nil
}

func (f *fakeClient) Nodes(context.Context, string) ([]engine.NodeStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.nodes, nil
}

func (f *fakeClient) TestNodes(_ context.Context, ids []string) (map[string]health.State, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.tests = append(f.tests, ids...)
	return map[string]health.State{ids[0]: {NodeID: ids[0], Checked: true, Alive: true, Latency: 42}}, nil
}

func (f *fakeClient) TestSub(_ context.Context, subID string) (map[string]health.State, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.tests = append(f.tests, "sub:"+subID)
	return map[string]health.State{"n1": {NodeID: "n1", Checked: true, Alive: true, Latency: 30}}, nil
}

func (f *fakeClient) Logs(_ context.Context, since uint64, _ int, _, _, _ string) ([]logs.Entry, uint64, error) {
	if f.err != nil {
		return nil, since, f.err
	}
	out := make([]logs.Entry, 0, len(f.entries))
	for _, e := range f.entries {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	last := since
	if len(out) > 0 {
		last = out[len(out)-1].Seq
	}
	return out, last, nil
}

func (f *fakeClient) RefreshSub(_ context.Context, id string) (*engine.SubStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.refreshes = append(f.refreshes, id)
	sub := f.subs[0]
	return &sub, nil
}

func (f *fakeClient) RestartCore(context.Context) error {
	if f.err != nil {
		return f.err
	}
	f.restarts++
	return nil
}

func (f *fakeClient) UpdateUser(_ context.Context, id string, body map[string]any) (*engine.UserStatus, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.patches = append(f.patches, userPatch{id: id, body: body})
	return nil, nil
}

func (f *fakeClient) ResetUserTraffic(_ context.Context, id string) error {
	if f.err != nil {
		return f.err
	}
	f.resets = append(f.resets, id)
	return nil
}

// --- fixtures -------------------------------------------------------------

func sampleStatus() *client.StatusResponse {
	return &client.StatusResponse{
		Status: engine.Status{
			Running:   true,
			UptimeSec: 3725,
			Restarts:  2,
			Totals:    config.Traffic{Up: 1 << 20, Down: 3 << 20},
			Users: []engine.UserStatus{{
				ID: "u1", Name: "alice", Enabled: true, Mode: config.ModePriority,
				Traffic:   config.Traffic{Up: 1 << 20, Down: 2 << 20},
				Online:    2,
				OnlineIPs: []string{"10.0.0.1", "10.0.0.2"},
			}},
		},
		Version: map[string]any{"version": "dev", "xray": "1.26.3"},
		Proxy: map[string]any{
			"http": "0.0.0.0:8080", "http_on": true,
			"socks": "0.0.0.0:1080", "socks_on": true, "socks_udp": true,
			"fallback": "reject",
		},
	}
}

func sampleUsers() []engine.UserStatus {
	return []engine.UserStatus{{
		ID: "u1", Name: "alice", Enabled: true, Mode: config.ModePriority,
		Fallback: config.FallbackReject, Password: "s3cret",
		Targets: []config.ResolvedTarget{
			{Sub: "s1", SubName: "机场A", NodeID: "n1", NodeName: "香港01", Type: "vless"},
			{Sub: "s1", SubName: "机场A", NodeName: "已下线", Missing: true},
		},
		Missing:   []string{"日本02（Xray 内核不支持 hysteria2 协议）"},
		Traffic:   config.Traffic{Up: 1 << 20, Down: 2 << 20},
		Online:    2,
		OnlineIPs: []string{"10.0.0.1", "10.0.0.2"},
	}}
}

func sampleSubs() []engine.SubStatus {
	return []engine.SubStatus{{
		Subscription: config.Subscription{
			ID: "s1", Name: "机场A", Kind: "clash", Enabled: true,
			LastUpdate: time.Now().Add(-3 * time.Minute), LastStatus: "ok",
		},
		CachedNodes: 3, CachedUsable: 2,
	}}
}

func sampleNodes() []engine.NodeStatus {
	return []engine.NodeStatus{
		{
			Node: node.Node{ID: "n1", Name: "香港01", Type: "vless", SubID: "s1",
				Server: "hk.example.com", Port: 443},
			SubName: "机场A",
			Health:  health.State{NodeID: "n1", Checked: true, Alive: true, Latency: 42},
			UsedBy:  []string{"alice"}, InUse: true,
		},
		{
			Node: node.Node{ID: "n2", Name: "日本02", Type: "hysteria2", SubID: "s1",
				Unsupported: "Xray 内核不支持 hysteria2 协议"},
			SubName: "机场A",
		},
	}
}

func sampleEntries() []logs.Entry {
	now := time.Now()
	return []logs.Entry{
		{Seq: 1, Time: now.Add(-time.Second), Level: "info", Source: "v2h", Msg: "内核已启动"},
		{Seq: 2, Time: now, Level: "error", Source: "v2h", User: "alice", Msg: "订阅拉取失败"},
	}
}

// --- helpers --------------------------------------------------------------

func newTestModel(api Client) *model {
	m := newModel(context.Background(), api, Options{BaseURL: "127.0.0.1:9080", Token: "secret"})
	m.ui = newStyles(true) // keep assertions free of escape sequences
	return m
}

func keyMsg(key string) tea.KeyMsg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "tab":
		return tea.KeyMsg{Type: tea.KeyTab}
	case " ":
		return tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
}

// press sends one keystroke and returns the command the model asked for.
func press(m *model, key string) tea.Cmd {
	_, cmd := m.Update(keyMsg(key))
	return cmd
}

// drain runs a command tree to completion and feeds the resulting messages
// back into the model, so the fake client records the call and the notice
// line appears. Tick commands are dropped: they only re-arm timers.
func drain(t *testing.T, m *model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, sub := range batch {
			drain(t, m, sub)
		}
		return
	}
	if _, ok := msg.(tickMsg); ok {
		return
	}
	m.Update(msg)
}

func assertContains(t *testing.T, view string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q\n----\n%s\n----", want, view)
		}
	}
}

func assertMissing(t *testing.T, view string, unwanted ...string) {
	t.Helper()
	for _, bad := range unwanted {
		if strings.Contains(view, bad) {
			t.Errorf("view should not contain %q\n----\n%s\n----", bad, view)
		}
	}
}

// --- tests ----------------------------------------------------------------

// TestEmptyStateRenders is the first thing a new user sees: a server with no
// accounts, no subscriptions and no logs.
func TestEmptyStateRenders(t *testing.T) {
	api := &fakeClient{status: &client.StatusResponse{Status: engine.Status{}}}
	m := newTestModel(api)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(statusMsg{resp: api.status})
	m.Update(usersMsg{})
	m.Update(subsMsg{})
	m.Update(nodesMsg{})
	m.Update(entriesMsg{})

	for v := view(0); v < viewCount; v++ {
		m.current = v
		view := m.View()
		if strings.TrimSpace(view) == "" {
			t.Fatalf("view %d rendered nothing", v)
		}
		assertContains(t, view, "v2h 终端控制台", "概览", "账号", "订阅", "节点", "日志", "帮助")
	}

	m.current = viewOverview
	assertContains(t, m.View(), "已停止", "累计流量")

	m.current = viewUsers
	assertContains(t, m.View(), "还没有账号")

	m.current = viewSubs
	assertContains(t, m.View(), "还没有订阅")

	m.current = viewNodes
	assertContains(t, m.View(), "还没有节点")

	m.current = viewLogs
	assertContains(t, m.View(), "暂无日志")

	// The same screens must survive a terminal that was never resized and one
	// too narrow to show every column.
	for _, size := range []tea.WindowSizeMsg{{Width: 0, Height: 0}, {Width: 28, Height: 6}} {
		small := newTestModel(api)
		small.Update(size)
		for v := view(0); v < viewCount; v++ {
			small.current = v
			if strings.TrimSpace(small.View()) == "" {
				t.Fatalf("view %d rendered nothing at %dx%d", v, size.Width, size.Height)
			}
		}
	}
}

func TestViewsRenderServerDataAndActions(t *testing.T) {
	api := &fakeClient{
		status:  sampleStatus(),
		users:   sampleUsers(),
		subs:    sampleSubs(),
		nodes:   sampleNodes(),
		entries: sampleEntries(),
	}
	m := newTestModel(api)
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m.Update(statusMsg{resp: api.status})
	m.Update(usersMsg{users: api.users})
	m.Update(subsMsg{subs: api.subs})
	m.Update(nodesMsg{nodes: api.nodes})
	m.Update(entriesMsg{entries: api.entries, last: 2})

	// 概览
	m.current = viewOverview
	assertContains(t, m.View(), "运行中", "运行时长", "1 小时", "HTTP 代理", "0.0.0.0:8080（已启用）", "拒绝连接", "上行 1.00 MB", "alice")

	// 账号
	m.current = viewUsers
	assertContains(t, m.View(), "alice", "已启用", "按优先级", "http://alice:s3cret@127.0.0.1:8080")

	// Enter opens the detail pane with targets, missing targets and exports.
	m.Update(keyMsg("enter"))
	assertContains(t, m.View(),
		"目标（按优先级）", "香港01", "失效/无法解析的目标",
		"hysteria2", "s3cret", "socks5://alice:s3cret@127.0.0.1:1080", "账号 alice")
	m.Update(keyMsg("esc"))
	assertMissing(t, m.View(), "目标（按优先级）")

	// space toggles the account through the API.
	drain(t, m, press(m, " "))
	if len(api.patches) != 1 {
		t.Fatalf("expected one PATCH, got %d", len(api.patches))
	}
	if api.patches[0].id != "u1" || api.patches[0].body["enabled"] != false {
		t.Fatalf("unexpected PATCH: %+v", api.patches[0])
	}
	if !strings.Contains(m.View(), "账号「alice」已停用") {
		t.Errorf("toggle notice missing:\n%s", m.View())
	}

	// t asks before resetting the traffic counter.
	m.Update(keyMsg("t"))
	if !strings.Contains(m.View(), "确认重置账号「alice」的流量计数") {
		t.Fatalf("no confirmation prompt:\n%s", m.View())
	}
	drain(t, m, press(m, "y"))
	if len(api.resets) != 1 || api.resets[0] != "u1" {
		t.Fatalf("traffic reset not sent: %+v", api.resets)
	}

	// r on the overview asks before restarting the core.
	m.current = viewOverview
	m.Update(keyMsg("r"))
	if !strings.Contains(m.View(), "确认重启代理内核") {
		t.Fatalf("no restart confirmation:\n%s", m.View())
	}
	drain(t, m, press(m, "y"))
	if api.restarts != 1 {
		t.Fatalf("core not restarted: %d", api.restarts)
	}

	// 订阅: expand a row and refresh it.
	m.current = viewSubs
	assertContains(t, m.View(), "机场A", "Clash", "正常", "3", "2")
	m.Update(keyMsg("enter"))
	assertContains(t, m.View(), "香港01", "hk.example.com:443")
	drain(t, m, press(m, "R"))
	if len(api.refreshes) != 1 || api.refreshes[0] != "s1" {
		t.Fatalf("refresh all did not run: %+v", api.refreshes)
	}

	// 节点: grouped by subscription, with the unsupported reason kept.
	m.current = viewNodes
	nodesView := m.View()
	assertContains(t, nodesView, "▸ 机场A（2 个节点）", "香港01", "vless", "hk.example.com:443", "42 ms", "alice", "日本02", "Xray 内核不支持 hysteria2 协议")

	drain(t, m, press(m, "t"))
	if len(api.tests) != 1 || api.tests[0] != "n1" {
		t.Fatalf("node test not sent: %+v", api.tests)
	}
	m.Update(keyMsg("down"))
	drain(t, m, press(m, "T"))
	if len(api.tests) != 2 || api.tests[1] != "sub:s1" {
		t.Fatalf("subscription test not sent: %+v", api.tests)
	}

	// 日志
	m.current = viewLogs
	assertContains(t, m.View(), "内核已启动", "订阅拉取失败", "跟随：开", "等级：全部")
	m.Update(keyMsg("a"))
	assertContains(t, m.View(), "等级：debug")
	m.Update(keyMsg("/"))
	m.Update(keyMsg("x"))
	assertContains(t, m.View(), "搜索：x_")
	m.Update(keyMsg("esc"))
	m.Update(keyMsg("f"))
	assertContains(t, m.View(), "跟随：关")
	m.Update(keyMsg("c"))
	assertContains(t, m.View(), "暂无日志")

	// 帮助
	m.current = viewHelp
	assertContains(t, m.View(), "按键说明", "使用步骤", "http://127.0.0.1:9080", "已使用 API Token 认证")

	// The ? overlay works on top of any view, and q quits the console only
	// (the server keeps running).
	m.current = viewOverview
	m.Update(keyMsg("?"))
	if !m.overlay {
		t.Fatal("? should open the help overlay")
	}
	assertContains(t, m.View(), "再按 ? 或 Esc 关闭")
	m.Update(keyMsg("?"))
	if m.overlay {
		t.Error("? should close the help overlay again")
	}
	m.Update(keyMsg("tab"))
	if m.current != viewUsers {
		t.Errorf("tab should move to the next view, got %d", m.current)
	}
	if cmd := press(m, "q"); cmd == nil {
		t.Error("q should quit the console")
	}

	// A rejected token stops the polls and points at the config file.
	tokenErr := &client.APIError{Status: 401, Message: "令牌无效"}
	rejected := newTestModel(&fakeClient{err: tokenErr})
	rejected.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	rejected.Update(statusMsg{err: tokenErr})
	assertContains(t, rejected.View(), "API Token 无效，请检查 config.yaml 里的 panel.api_token")
	if rejected.polling() {
		t.Error("polling should stop while the token is rejected")
	}
	press(rejected, "r")
	if !rejected.polling() {
		t.Error("r should resume polling after an invalid token")
	}

	// A dead server keeps the last known data on screen and backs off.
	offlineErr := errors.New("连接 http://127.0.0.1:9080 失败：dial tcp 127.0.0.1:9080: connect: connection refused")
	offline := newTestModel(&fakeClient{err: offlineErr})
	offline.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	offline.Update(statusMsg{resp: sampleStatus()})
	offline.Update(usersMsg{users: sampleUsers()})
	offline.Update(statusMsg{err: offlineErr})
	assertContains(t, offline.View(), "请求失败", "connection refused")
	offline.current = viewUsers
	assertContains(t, offline.View(), "alice")
	if offline.polling() {
		t.Error("a connection failure should back off before the next try")
	}
}
