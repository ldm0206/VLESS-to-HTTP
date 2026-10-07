package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/health"
)

// handleKey routes a keystroke. Search input and confirmation prompts are
// modal: while they are up they own the keyboard.
func (m *model) handleKey(msg tea.KeyMsg) tea.Cmd {
	if m.searching {
		return m.handleSearchKey(msg)
	}
	if m.confirm != nil {
		return m.handleConfirmKey(msg.String())
	}

	key := msg.String()
	switch key {
	case "ctrl+c", "q":
		// The console is only a client: quitting leaves the server running.
		return tea.Quit
	case "?":
		m.overlay = !m.overlay
		return nil
	case "esc":
		if m.overlay {
			m.overlay = false
			return nil
		}
		m.userDetail = false
		return nil
	case "1", "2", "3", "4", "5", "6":
		return m.switchView(view(key[0] - '1'))
	case "tab":
		return m.switchView((m.current + 1) % viewCount)
	case "shift+tab":
		return m.switchView((m.current + viewCount - 1) % viewCount)
	}

	// A rejected token stops every poll; r is the way back in.
	if key == "r" && m.unauthorized {
		return m.retry()
	}
	if m.overlay {
		return nil
	}

	switch key {
	case "up", "k":
		m.moveCursor(-1, m.cursorLen())
		return nil
	case "down", "j":
		m.moveCursor(1, m.cursorLen())
		return nil
	case "pgup":
		m.moveCursor(-10, m.cursorLen())
		return nil
	case "pgdown":
		m.moveCursor(10, m.cursorLen())
		return nil
	case "g":
		m.jumpCursor(true)
		return nil
	case "G":
		m.jumpCursor(false)
		return nil
	}

	switch m.current {
	case viewOverview:
		return m.keyOverview(key)
	case viewUsers:
		return m.keyUsers(key)
	case viewSubs:
		return m.keySubs(key)
	case viewNodes:
		return m.keyNodes(key)
	case viewLogs:
		return m.keyLogs(key)
	}
	return nil
}

func (m *model) cursorLen() int {
	switch m.current {
	case viewUsers:
		return len(m.users)
	case viewSubs:
		return len(m.subs)
	case viewNodes:
		return len(m.nodes)
	case viewLogs:
		return len(m.entries)
	}
	return 0
}

// jumpCursor moves to the first (top) or last row of the active list.
func (m *model) jumpCursor(first bool) {
	n := m.cursorLen()
	if n == 0 {
		return
	}
	last := n - 1
	switch m.current {
	case viewUsers:
		if first {
			m.userIdx = 0
		} else {
			m.userIdx = last
		}
	case viewSubs:
		if first {
			m.subIdx = 0
		} else {
			m.subIdx = last
		}
	case viewNodes:
		if first {
			m.nodeIdx = 0
		} else {
			m.nodeIdx = last
		}
	case viewLogs:
		if first {
			m.follow = false
			m.scroll = last
		} else {
			m.follow = true
			m.scroll = 0
		}
	}
}

func (m *model) switchView(v view) tea.Cmd {
	if v == m.current || v < 0 || v >= viewCount {
		return nil
	}
	m.current = v
	m.overlay = false
	m.notice = ""
	m.clampCursors()
	return tea.Batch(m.pollStatus(), m.pollView(), m.pollLogs())
}

// --- 概览 ------------------------------------------------------------------

func (m *model) keyOverview(key string) tea.Cmd {
	if key == "r" {
		m.confirm = &confirmState{
			kind:   confirmRestartCore,
			prompt: "确认重启代理内核？（只重启内核，不会停止服务）",
		}
	}
	return nil
}

// --- 账号 ------------------------------------------------------------------

func (m *model) keyUsers(key string) tea.Cmd {
	u := m.selectedUser()
	if u == nil {
		return nil
	}
	switch key {
	case "enter":
		m.userDetail = true
	case "esc":
		m.userDetail = false
	case " ", "space":
		return m.toggleUser(*u)
	case "t":
		m.confirm = &confirmState{
			kind:   confirmResetTraffic,
			prompt: fmt.Sprintf("确认重置账号「%s」的流量计数？", u.Name),
			userID: u.ID,
		}
	}
	return nil
}

// toggleUser flips one account on or off with a PATCH.
func (m *model) toggleUser(u engine.UserStatus) tea.Cmd {
	next := !u.Enabled
	action, done := "启用", fmt.Sprintf("账号「%s」已启用", u.Name)
	if !next {
		action, done = "停用", fmt.Sprintf("账号「%s」已停用", u.Name)
	}
	start := m.startAction(fmt.Sprintf("正在%s账号「%s」…", action, u.Name))

	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		_, err := api.UpdateUser(ctx, u.ID, map[string]any{"enabled": next})
		return actionMsg{done: done, label: "切换账号状态", err: err}
	})
}

// --- 订阅 ------------------------------------------------------------------

func (m *model) keySubs(key string) tea.Cmd {
	sub := m.selectedSub()
	switch key {
	case "enter":
		if sub != nil {
			m.expanded[sub.ID] = !m.expanded[sub.ID]
		}
	case "r":
		if sub != nil {
			return m.refreshSub(*sub)
		}
	case "R":
		if len(m.subs) > 0 {
			return m.refreshAllSubs()
		}
	}
	return nil
}

func (m *model) refreshSub(sub engine.SubStatus) tea.Cmd {
	start := m.startAction(fmt.Sprintf("正在刷新订阅「%s」…", sub.Name))
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		updated, err := api.RefreshSub(ctx, sub.ID)
		if err != nil {
			return actionMsg{label: "刷新订阅「" + sub.Name + "」", err: err}
		}
		nodes := 0
		if updated != nil {
			nodes = updated.CachedNodes
		}
		return actionMsg{done: fmt.Sprintf("订阅「%s」已刷新，共 %d 个节点", sub.Name, nodes)}
	})
}

func (m *model) refreshAllSubs() tea.Cmd {
	subs := append([]engine.SubStatus(nil), m.subs...)
	start := m.startAction(fmt.Sprintf("正在刷新全部 %d 条订阅…", len(subs)))
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		ok, failed := 0, 0
		var lastErr error
		for _, s := range subs {
			if _, err := api.RefreshSub(ctx, s.ID); err != nil {
				failed++
				lastErr = err
				continue
			}
			ok++
		}
		if failed > 0 {
			return actionMsg{
				label: "刷新全部订阅",
				err:   fmt.Errorf("%d 条失败，最后一条：%v", failed, lastErr),
			}
		}
		return actionMsg{done: fmt.Sprintf("已刷新 %d 条订阅", ok)}
	})
}

// --- 节点 ------------------------------------------------------------------

func (m *model) keyNodes(key string) tea.Cmd {
	n := m.selectedNode()
	switch key {
	case "t":
		if n != nil {
			return m.testNode(*n)
		}
	case "T":
		if n != nil {
			return m.testSubNodes(n.SubID, n.SubName)
		}
	}
	return nil
}

func (m *model) testNode(n engine.NodeStatus) tea.Cmd {
	label := n.Label()
	start := m.startAction(fmt.Sprintf("正在测试节点「%s」…", label))
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		results, err := api.TestNodes(ctx, []string{n.ID})
		if err != nil {
			return actionMsg{label: "节点测速", err: err}
		}
		return actionMsg{done: fmt.Sprintf("节点「%s」：%s", label, testSummary(results))}
	})
}

func (m *model) testSubNodes(subID, subName string) tea.Cmd {
	// The server tests the whole subscription in parallel; the console only
	// shows a spinner until it answers.
	start := m.startAction(fmt.Sprintf("正在测试订阅「%s」的全部节点…", subName))
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		results, err := api.TestSub(ctx, subID)
		if err != nil {
			return actionMsg{label: "订阅测速", err: err}
		}
		return actionMsg{done: fmt.Sprintf("订阅「%s」：%s", subName, testSummary(results))}
	})
}

// testSummary turns a latency test result map into one Chinese line.
func testSummary(results map[string]health.State) string {
	if len(results) == 0 {
		return "没有可测试的节点"
	}
	alive, dead, best := 0, 0, int64(0)
	for _, st := range results {
		if st.Alive {
			alive++
			if st.Latency > 0 && (best == 0 || st.Latency < best) {
				best = st.Latency
			}
			continue
		}
		dead++
	}
	text := fmt.Sprintf("%d 个节点可用", alive)
	if dead > 0 {
		text += fmt.Sprintf("，%d 个不可用", dead)
	}
	if best > 0 {
		text += fmt.Sprintf("，最快 %d ms", best)
	}
	return text
}

// --- 日志 ------------------------------------------------------------------

func (m *model) keyLogs(key string) tea.Cmd {
	switch key {
	case "f":
		m.follow = !m.follow
		if m.follow {
			m.scroll = 0
		}
	case "a":
		m.levelIdx = (m.levelIdx + 1) % len(logLevels)
		m.resetLogs()
	case "c":
		m.entries = nil
		m.scroll = 0
	case "/":
		m.searching = true
		m.draft = m.query
	}
	return nil
}

func (m *model) handleSearchKey(msg tea.KeyMsg) tea.Cmd {
	switch msg.Type {
	case tea.KeyCtrlC:
		return tea.Quit
	case tea.KeyEnter:
		m.searching = false
		m.query = strings.TrimSpace(m.draft)
		m.resetLogs()
	case tea.KeyEsc:
		m.searching = false
		m.draft = m.query
	case tea.KeyBackspace:
		if m.draft != "" {
			r := []rune(m.draft)
			m.draft = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		m.draft += " "
	case tea.KeyRunes:
		m.draft += string(msg.Runes)
	}
	return nil
}

// --- confirmation ---------------------------------------------------------

func (m *model) handleConfirmKey(key string) tea.Cmd {
	c := m.confirm
	switch key {
	case "ctrl+c":
		return tea.Quit
	case "y", "Y":
		m.confirm = nil
		switch c.kind {
		case confirmRestartCore:
			return m.restartCore()
		case confirmResetTraffic:
			return m.resetTraffic(c.userID)
		}
	case "n", "N", "q", "esc":
		m.confirm = nil
		m.notice = "已取消"
		m.noticeBad = false
	}
	return nil
}

func (m *model) restartCore() tea.Cmd {
	start := m.startAction("正在重启代理内核…")
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		if err := api.RestartCore(ctx); err != nil {
			return actionMsg{label: "重启内核", err: err}
		}
		return actionMsg{done: "内核已重启"}
	})
}

func (m *model) resetTraffic(userID string) tea.Cmd {
	name := userID
	for _, u := range m.users {
		if u.ID == userID {
			name = u.Name
			break
		}
	}
	start := m.startAction(fmt.Sprintf("正在重置账号「%s」的流量…", name))
	api, ctx := m.api, m.ctx
	return tea.Batch(start, func() tea.Msg {
		if err := api.ResetUserTraffic(ctx, userID); err != nil {
			return actionMsg{label: "重置流量", err: err}
		}
		return actionMsg{done: fmt.Sprintf("账号「%s」的流量计数已重置", name)}
	})
}
