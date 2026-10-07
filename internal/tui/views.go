package tui

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/help"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/version"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// logLevels is the cycle behind the a key. The empty value asks the server
// for its configured default, which is the widest filter.
var logLevels = []struct{ value, label string }{
	{"", "全部"},
	{"debug", "debug"},
	{"info", "info"},
	{"warning", "warning"},
	{"error", "error"},
}

func (m *model) logLevel() string {
	if m.levelIdx < 0 || m.levelIdx >= len(logLevels) {
		return ""
	}
	return logLevels[m.levelIdx].value
}

func (m *model) logLevelLabel() string {
	if m.levelIdx < 0 || m.levelIdx >= len(logLevels) {
		return "全部"
	}
	return logLevels[m.levelIdx].label
}

// View draws the whole screen: title, tab bar, banners, body and footer.
func (m *model) View() string {
	w, h := m.dims()
	banners := m.renderBanners(w)
	footer := m.footerLines(w)

	bodyHeight := h - 2 - len(banners) - len(footer)
	if bodyHeight < 3 {
		bodyHeight = 3
	}

	body := m.bodyView(w, bodyHeight)
	if m.overlay {
		body = m.overlayView(w, bodyHeight)
	}

	lines := make([]string, 0, bodyHeight+6)
	lines = append(lines, m.renderTitle(w), m.renderTabs(w))
	lines = append(lines, banners...)
	lines = append(lines, strings.Split(body, "\n")...)
	lines = append(lines, footer...)
	return strings.Join(lines, "\n")
}

func (m *model) bodyView(w, h int) string {
	switch m.current {
	case viewOverview:
		return m.viewOverview(w, h)
	case viewUsers:
		return m.viewUsers(w, h)
	case viewSubs:
		return m.viewSubs(w, h)
	case viewNodes:
		return m.viewNodes(w, h)
	case viewLogs:
		return m.viewLogs(w, h)
	default:
		return m.viewHelp(w, h)
	}
}

// --- chrome ---------------------------------------------------------------

func (m *model) renderTitle(w int) string {
	left := fmt.Sprintf("v2h 终端控制台 · v2h %s · Xray %s", version.Full(), version.Xray())
	right := ""
	if m.api != nil {
		right = "API " + m.api.Base()
	}
	line := left
	if right != "" && lipgloss.Width(left)+lipgloss.Width(right)+2 <= w {
		line = left + strings.Repeat(" ", w-lipgloss.Width(left)-lipgloss.Width(right)) + right
	}
	return m.ui.title.Render(truncate(line, w))
}

func (m *model) renderTabs(w int) string {
	parts := make([]string, 0, viewCount)
	used := 0
	for i, name := range viewTitles {
		label := fmt.Sprintf(" %d %s ", i+1, name)
		width := lipgloss.Width(label)
		if len(parts) > 0 {
			width++
		}
		if used+width > w && len(parts) > 0 {
			break
		}
		used += width
		if view(i) == m.current {
			parts = append(parts, m.ui.tabOn.Render(label))
		} else {
			parts = append(parts, m.ui.tab.Render(label))
		}
	}
	return strings.Join(parts, " ")
}

// renderBanners shows the connection trouble without dropping the last known
// data: a rejected token or an unreachable server.
func (m *model) renderBanners(w int) []string {
	var out []string
	if m.unauthorized {
		text := "⚠ API Token 无效，请检查 config.yaml 里的 panel.api_token（按 r 重新连接）"
		out = append(out, m.ui.bad.Render(truncate(text, w)))
	}
	if m.lastErr != "" {
		text := "⚠ 请求失败：" + m.lastErr
		if wait := time.Until(m.nextRetry); wait > 0 {
			text += fmt.Sprintf("（%s 后自动重试）", wait.Round(time.Second))
		}
		out = append(out, m.ui.warn.Render(truncate(text, w)))
	}
	if len(out) > 0 {
		out = append(out, "")
	}
	return out
}

func (m *model) footerLines(w int) []string {
	out := make([]string, 0, 2)
	switch {
	case m.confirm != nil:
		text := fmt.Sprintf("%s  (y 确认 / n 取消)", m.confirm.prompt)
		out = append(out, m.ui.warn.Render(truncate(text, w)))
	case m.busyAction:
		out = append(out, m.ui.accent.Render(truncate(m.spinner()+" "+m.actionText, w)))
	case m.notice != "":
		style := m.ui.good
		if m.noticeBad {
			style = m.ui.bad
		}
		out = append(out, style.Render(truncate(m.notice, w)))
	}
	out = append(out, m.ui.footer.Render(truncate(m.hintLine(), w)))
	return out
}

func (m *model) spinner() string {
	if len(spinnerFrames) == 0 {
		return "*"
	}
	idx := 0
	if m.spinFrame >= 0 {
		idx = m.spinFrame % len(spinnerFrames)
	}
	return spinnerFrames[idx]
}

// hintLine is the per-view key reminder in the footer.
func (m *model) hintLine() string {
	const quit = "q 退出（不会停止服务）"
	switch m.current {
	case viewOverview:
		return "r 重启内核 · ? 帮助 · " + quit
	case viewUsers:
		if m.userDetail {
			return "Esc 返回列表 · space 启用/停用 · t 重置流量 · " + quit
		}
		return "↑↓ 选择 · Enter 详情 · space 启用/停用 · t 重置流量 · " + quit
	case viewSubs:
		return "↑↓ 选择 · Enter 展开节点 · r 刷新选中 · R 刷新全部 · " + quit
	case viewNodes:
		return "↑↓ 选择 · t 测试节点 · T 测试整条订阅 · " + quit
	case viewLogs:
		return fmt.Sprintf("f 跟随：%s · a 等级：%s · / 搜索 · c 清屏 · %s",
			onOff(m.follow), m.logLevelLabel(), quit)
	default:
		return "? 关闭帮助 · " + quit
	}
}

// --- 概览 ------------------------------------------------------------------

func (m *model) viewOverview(w, h int) string {
	if m.status == nil {
		return fitTop([]string{m.ui.dim.Render("正在读取服务状态…")}, h)
	}
	st := m.status.Status
	proxy := m.status.Proxy

	lines := make([]string, 0, 24)
	state, stateStyle := "已停止", m.ui.bad
	if st.Running {
		state, stateStyle = "运行中", m.ui.good
	}
	lines = append(lines, m.kv("内核状态", state, stateStyle, w))
	if st.LastError != "" {
		lines = append(lines, m.kv("最近错误", st.LastError, m.ui.bad, w))
	}
	lines = append(lines, m.kv("运行时长", humanDuration(time.Duration(st.UptimeSec)*time.Second), lipgloss.NewStyle(), w))
	lines = append(lines, m.kv("重启次数", fmt.Sprintf("%d 次", st.Restarts), lipgloss.NewStyle(), w))
	lines = append(lines, m.kv("HTTP 代理", listenLine(proxy, "http", "http_on"), lipgloss.NewStyle(), w))
	lines = append(lines, m.kv("SOCKS5", listenLine(proxy, "socks", "socks_on", "socks_udp"), lipgloss.NewStyle(), w))
	lines = append(lines, m.kv("兜底策略", fallbackLabel(stringField(proxy, "fallback")), lipgloss.NewStyle(), w))
	lines = append(lines, m.kv("累计流量",
		fmt.Sprintf("上行 %s / 下行 %s", client.FormatBytes(st.Totals.Up), client.FormatBytes(st.Totals.Down)),
		lipgloss.NewStyle(), w))
	if st.NodeSummary.Total > 0 {
		lines = append(lines, m.kv("节点健康",
			fmt.Sprintf("共 %d 个：可用 %d，不可用 %d", st.NodeSummary.Total, st.NodeSummary.Alive, st.NodeSummary.Dead),
			lipgloss.NewStyle(), w))
	}
	if st.Dropped > 0 {
		lines = append(lines, m.kv("丢弃日志", fmt.Sprintf("%d 条", st.Dropped), m.ui.warn, w))
	}

	lines = append(lines, "", m.ui.section.Render("账号摘要"))
	lines = append(lines, m.summaryTable(w)...)
	return fitTop(lines, h)
}

// summaryTable is the compact per-account block on the overview.
func (m *model) summaryTable(w int) []string {
	if m.status == nil || len(m.status.Status.Users) == 0 {
		return []string{m.ui.dim.Render("  还没有账号，详情见「账号」页")}
	}
	cols := []tcol{
		{title: "名称", prio: 3, flex: true},
		{title: "状态", prio: 3, flex: true},
		{title: "上行", prio: 1},
		{title: "下行", prio: 1},
		{title: "在线", prio: 0},
	}
	rows := make([][]string, 0, len(m.status.Status.Users))
	for _, u := range m.status.Status.Users {
		rows = append(rows, []string{
			u.Name,
			client.UserStatusSummary(u),
			client.FormatBytes(u.Traffic.Up),
			client.FormatBytes(u.Traffic.Down),
			onlineLabel(u.Online),
		})
	}
	lines := renderTable(cols, rows, w-2)
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	if len(lines) > 0 {
		lines[0] = m.ui.section.Render(lines[0])
	}
	return lines
}

// --- 账号 ------------------------------------------------------------------

func (m *model) viewUsers(w, h int) string {
	if m.userDetail {
		if u := m.selectedUser(); u != nil {
			return fitTop(m.userDetailLines(*u, w), h)
		}
	}
	if len(m.users) == 0 {
		return fitTop([]string{
			m.ui.section.Render("还没有账号"),
			m.ui.dim.Render("用 `v2h user add <名字> --password <密码> --target \"订阅:节点\"` 创建，"),
			m.ui.dim.Render("或在 Web 面板的「账号」页添加。"),
		}, h)
	}

	cols := []tcol{
		{title: "名称", prio: 3, flex: true},
		{title: "状态", prio: 3},
		{title: "模式", prio: 1},
		{title: "出口", prio: 2, flex: true},
		{title: "上行", prio: 1},
		{title: "下行", prio: 1},
		{title: "在线", prio: 0},
	}
	rows := make([][]string, 0, len(m.users))
	for _, u := range m.users {
		rows = append(rows, []string{
			u.Name,
			enabledLabel(u.Enabled),
			modeLabel(u.Mode),
			client.UserStatusSummary(u),
			client.FormatBytes(u.Traffic.Up),
			client.FormatBytes(u.Traffic.Down),
			onlineLabel(u.Online),
		})
	}

	lines := renderTable(cols, rows, w)
	if len(lines) > 0 {
		lines[0] = m.ui.section.Render(lines[0])
	}
	for i := 1; i < len(lines); i++ {
		if i-1 == m.userIdx {
			lines[i] = m.ui.selected.Render(lines[i])
		}
	}

	if u := m.selectedUser(); u != nil {
		if exports := m.exportLines(*u); len(exports) > 0 {
			lines = append(lines, "")
			lines = append(lines, m.ui.footer.Render(truncate("导出 "+exports[0], w)))
		}
	}
	return fitTop(lines, h)
}

// userDetailLines is the Enter pane: targets in priority order, the ones the
// core could not resolve, the credentials and the last error.
func (m *model) userDetailLines(u engine.UserStatus, w int) []string {
	style := lipgloss.NewStyle()
	lines := []string{
		m.ui.section.Render("账号 " + u.Name + "（Esc 返回列表）"),
		m.kv("状态", enabledLabel(u.Enabled), boolStyle(u.Enabled, m.ui.good, m.ui.bad), w),
		m.kv("模式", modeLabel(u.Mode), style, w),
		m.kv("兜底策略", fallbackLabel(u.Fallback), style, w),
		m.kv("密码", u.Password, m.ui.accent, w),
		m.kv("在线", onlineDetail(u), style, w),
	}
	if u.Note != "" {
		lines = append(lines, m.kv("备注", u.Note, style, w))
	}

	lines = append(lines, "", m.ui.section.Render("导出（可直接复制）"))
	exports := m.exportLines(u)
	if len(exports) == 0 {
		lines = append(lines, m.ui.dim.Render("  代理监听地址未配置，无法生成导出地址"))
	}
	for _, e := range exports {
		lines = append(lines, "  "+m.ui.accent.Render(truncate(e, w-2)))
	}

	lines = append(lines, "", m.ui.section.Render("目标（按优先级）"))
	if len(u.Targets) == 0 {
		lines = append(lines, m.ui.dim.Render("  未设置目标，全部流量走兜底策略"))
	}
	for i, t := range u.Targets {
		name := t.SubName + " / " + t.NodeName
		if t.NodeName == "" || t.NodeID == "" {
			lines = append(lines, fmt.Sprintf("  %d. %s", i+1, m.ui.dim.Render(truncate(name, w-6))))
			continue
		}
		state := ""
		if t.Type != "" {
			state = "（" + t.Type + "）"
		}
		text := truncate(fmt.Sprintf("%d. %s%s", i+1, name, state), w-2)
		if t.Missing {
			lines = append(lines, "  "+m.ui.gray(text+" 不可用"))
			continue
		}
		lines = append(lines, "  "+text)
	}

	if len(u.Missing) > 0 {
		lines = append(lines, "", m.ui.section.Render("失效/无法解析的目标"))
		for _, miss := range u.Missing {
			lines = append(lines, "  "+m.ui.bad.Render(truncate("- "+miss, w-2)))
		}
	}
	if u.Reason != "" {
		lines = append(lines, "", m.kv("路由说明", u.Reason, m.ui.dim, w))
	}
	if lastErr := m.userLastError(u); lastErr != "" {
		lines = append(lines, m.kv("最近错误", lastErr, m.ui.bad, w))
	}
	return lines
}

// userLastError prefers the route's own reason for why nothing is usable.
func (m *model) userLastError(u engine.UserStatus) string {
	if m.status != nil && m.status.Status.LastError != "" && !u.Enabled {
		return m.status.Status.LastError
	}
	if u.Reason != "" && len(u.Missing) > 0 {
		return u.Reason
	}
	return ""
}

// --- 订阅 ------------------------------------------------------------------

func (m *model) viewSubs(w, h int) string {
	if len(m.subs) == 0 {
		return fitTop([]string{
			m.ui.section.Render("还没有订阅"),
			m.ui.dim.Render("用 `v2h sub add <名字> --url <订阅地址>` 添加，"),
			m.ui.dim.Render("或在 Web 面板的「订阅」页粘贴 Clash / v2ray 链接。"),
		}, h)
	}

	cols := []tcol{
		{title: "名称", prio: 3, flex: true},
		{title: "格式", prio: 1},
		{title: "状态", prio: 3, flex: true},
		{title: "节点数", prio: 2},
		{title: "可用", prio: 2},
		{title: "上次更新", prio: 1},
	}
	rows := make([][]string, 0, len(m.subs))
	for _, s := range m.subs {
		status := subStatusLabel(s)
		rows = append(rows, []string{
			s.Name,
			client.SubModeLabel(s.Kind),
			status,
			fmt.Sprint(s.CachedNodes),
			fmt.Sprint(s.CachedUsable),
			relativeTime(s.LastUpdate),
		})
	}

	table := renderTable(cols, rows, w)
	out := make([]string, 0, len(table)+4)
	if len(table) > 0 {
		out = append(out, m.ui.section.Render(table[0]))
	}
	for i, s := range m.subs {
		if i+1 >= len(table) {
			break
		}
		line := table[i+1]
		if i == m.subIdx {
			line = m.ui.selected.Render(line)
		}
		out = append(out, line)
		if m.expanded[s.ID] {
			out = append(out, m.subNodeLines(s, w)...)
		}
	}

	if s := m.selectedSub(); s != nil {
		if s.Error != "" {
			out = append(out, "", m.ui.bad.Render(truncate("最近错误："+s.Error, w)))
		}
		if s.URL != "" {
			out = append(out, m.ui.footer.Render(truncate("地址："+s.URL, w)))
		}
	}
	return fitTop(out, h)
}

// subNodeLines lists the nodes of one expanded subscription.
func (m *model) subNodeLines(s engine.SubStatus, w int) []string {
	nodes := make([]engine.NodeStatus, 0, 8)
	for _, n := range m.nodes {
		if n.SubID == s.ID {
			nodes = append(nodes, n)
		}
	}
	if len(nodes) == 0 {
		return []string{"    " + m.ui.dim.Render("（暂无缓存节点，按 r 刷新订阅）")}
	}
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		state, latency := nodeState(n)
		text := fmt.Sprintf("    · %s  %s  %s:%d  %s %s", n.Name, n.Type, n.Server, n.Port, state, latency)
		text = truncate(strings.TrimSpace(text), w)
		if n.Unsupported != "" {
			text = m.ui.gray(text)
		}
		out = append(out, text)
	}
	return out
}

// --- 节点 ------------------------------------------------------------------

func (m *model) viewNodes(w, h int) string {
	if len(m.nodes) == 0 {
		return fitTop([]string{
			m.ui.section.Render("还没有节点"),
			m.ui.dim.Render("先在「订阅」页添加订阅，选中后按 r 拉取节点列表。"),
		}, h)
	}

	cols := []tcol{
		{title: "名称", prio: 3, flex: true},
		{title: "协议", prio: 2},
		{title: "地址", prio: 2, flex: true},
		{title: "延迟", prio: 1},
		{title: "状态", prio: 3, flex: true},
		{title: "使用者", prio: 0},
	}
	rows := make([][]string, 0, len(m.nodes))
	for _, n := range m.nodes {
		state, latency := nodeState(n)
		rows = append(rows, []string{
			n.Name,
			n.Type,
			fmt.Sprintf("%s:%d", n.Server, n.Port),
			latency,
			state,
			usersLabel(n.UsedBy),
		})
	}
	table := renderTable(cols, rows, w-2)

	out := make([]string, 0, len(table)+8)
	if len(table) > 0 {
		out = append(out, "  "+m.ui.section.Render(table[0]))
	}

	start := 0
	for start < len(m.nodes) {
		name := m.nodes[start].SubName
		end := start
		for end < len(m.nodes) && m.nodes[end].SubName == name {
			end++
		}
		if name == "" {
			name = "未分组"
		}
		out = append(out, m.ui.section.Render(fmt.Sprintf("▸ %s（%d 个节点）", name, end-start)))
		for i := start; i < end; i++ {
			line := table[i+1]
			switch {
			case i == m.nodeIdx:
				line = m.ui.selected.Render(line)
			case m.nodes[i].Unsupported != "":
				line = m.ui.gray(line)
			}
			out = append(out, "  "+line)
		}
		start = end
	}

	if n := m.selectedNode(); n != nil && n.Unsupported != "" {
		out = append(out, "", m.ui.gray(truncate("该节点不可用："+n.Unsupported, w)))
	}
	return fitTop(out, h)
}

// --- 日志 ------------------------------------------------------------------

func (m *model) viewLogs(w, h int) string {
	out := make([]string, 0, h)
	out = append(out, m.logHeader(w))
	if len(m.entries) == 0 {
		out = append(out, m.ui.dim.Render("（暂无日志）"))
		if m.searching {
			out = append(out, m.searchLine(w))
		}
		return fitTop(out, h)
	}

	height := h - 1
	if m.searching {
		height--
	}
	if height < 1 {
		height = 1
	}

	total := len(m.entries)
	end := total - m.scroll
	end = clamp(end, 0, total)
	begin := end - height
	if begin < 0 {
		begin = 0
	}
	window := make([]string, 0, end-begin)
	for _, e := range m.entries[begin:end] {
		window = append(window, m.logLine(e, w))
	}
	out = append(out, window...)
	if m.searching {
		out = append(out, m.searchLine(w))
	}
	return fitTop(out, h)
}

func (m *model) logHeader(w int) string {
	follow := m.ui.dim.Render("跟随：关")
	if m.follow {
		follow = m.ui.good.Render("跟随：开")
	}
	parts := []string{
		follow,
		m.ui.dim.Render("等级：" + m.logLevelLabel()),
		m.ui.dim.Render(fmt.Sprintf("共 %d 条（最多保留 %d 条）", len(m.entries), maxLogEntries)),
	}
	if m.query != "" {
		parts = append(parts, m.ui.accent.Render("关键字："+m.query))
	}
	if m.scroll > 0 {
		parts = append(parts, m.ui.warn.Render(fmt.Sprintf("向上翻 %d 行", m.scroll)))
	}
	return truncate(strings.Join(parts, " · "), w)
}

func (m *model) searchLine(w int) string {
	return m.ui.accent.Render(truncate("搜索："+m.draft+"_（Enter 确认，Esc 取消）", w))
}

func (m *model) logLine(e logs.Entry, w int) string {
	head := fmt.Sprintf("%s %s %s", e.Time.Format("01-02 15:04:05"), pad(e.Level, 7), pad(e.Source, 6))
	if e.User != "" {
		head += " [" + e.User + "]"
	}
	text := truncate(head+" "+e.Msg, w)
	return m.levelStyle(e.Level).Render(text)
}

func (m *model) levelStyle(level string) lipgloss.Style {
	switch strings.ToLower(level) {
	case "error":
		return m.ui.bad
	case "warning", "warn":
		return m.ui.warn
	case "debug":
		return m.ui.dim
	default:
		return lipgloss.NewStyle()
	}
}

// --- 帮助 ------------------------------------------------------------------

func (m *model) viewHelp(w, h int) string {
	lines := make([]string, 0, 32)
	lines = append(lines, m.ui.section.Render("按键说明"))
	for _, k := range help.TUIKeys {
		lines = append(lines, "  "+pad(k.Key, 14)+" "+truncate(k.Action, w-17))
	}

	lines = append(lines, "", m.ui.section.Render("使用步骤"))
	for _, step := range []string{
		"1. 在「订阅」页选中一条订阅按 r 拉取节点；没有订阅时先在 Web 面板添加。",
		"2. 在「账号」页用 space 启用账号，Enter 查看出口、目标和导出地址。",
		"3. 在「节点」页按 T 给整条订阅测速，t 只测光标所在的节点。",
		"4. 排查问题时到「日志」页，按 a 切换等级过滤，按 / 搜索关键字。",
	} {
		lines = append(lines, "  "+truncate(step, w-2))
	}

	lines = append(lines, "", m.ui.section.Render("连接信息"))
	base := ""
	token := "未使用（服务端未开启 Token 校验）"
	if m.api != nil {
		base = m.api.Base()
	}
	if m.opts.Token != "" {
		token = "已使用 API Token 认证"
	}
	style := lipgloss.NewStyle()
	lines = append(lines, m.kv("API 地址", base, style, w))
	lines = append(lines, m.kv("认证方式", token, style, w))
	lines = append(lines, m.kv("控制台版本", "v2h "+version.Full(), style, w))
	lines = append(lines, m.kv("Xray 内核", version.Xray(), style, w))
	if m.status != nil {
		lines = append(lines, m.kv("服务端版本", fmt.Sprintf("v2h %v · Xray %v",
			m.status.Version["version"], m.status.Version["xray"]), style, w))
	}
	lines = append(lines, m.kv("退出说明", "q / Ctrl+C 只关闭控制台，服务继续运行", m.ui.dim, w))
	return fitTop(lines, h)
}

// overlayView is the ? panel shown on top of any view.
func (m *model) overlayView(w, h int) string {
	inner := w - 6
	if inner > 78 {
		inner = 78
	}
	if inner < 20 {
		inner = 20
	}
	lines := make([]string, 0, len(help.TUIKeys)+4)
	lines = append(lines, m.ui.title.Render("按键说明"), "")
	for _, k := range help.TUIKeys {
		lines = append(lines, "  "+pad(k.Key, 14)+" "+truncate(k.Action, inner-17))
	}
	lines = append(lines, "", m.ui.dim.Render("再按 ? 或 Esc 关闭；q 退出控制台（服务继续运行）。"))

	box := m.ui.box.Render(strings.Join(lines, "\n"))
	return fitTop(strings.Split(box, "\n"), h)
}

// --- shared helpers -------------------------------------------------------

// kv renders one "标签  值" line of a detail block.
func (m *model) kv(key, value string, style lipgloss.Style, w int) string {
	const keyWidth = 12
	room := w - keyWidth - 1
	if room < 8 {
		room = 8
	}
	if value == "" {
		value = "—"
	}
	return m.ui.dim.Render(pad(key, keyWidth)) + " " + style.Render(truncate(value, room))
}

// exportLines builds the copy-ready proxy URLs for one account, e.g.
// "HTTP: http://alice:secret@127.0.0.1:8080".
func (m *model) exportLines(u engine.UserStatus) []string {
	out := make([]string, 0, 2)
	add := func(scheme, listen string) {
		host, port := clientHost(listen)
		if host == "" {
			return
		}
		link := url.URL{
			Scheme: scheme,
			Host:   net.JoinHostPort(host, port),
			User:   url.UserPassword(u.Name, u.Password),
		}
		out = append(out, strings.ToUpper(scheme)+": "+link.String())
	}
	add("http", m.proxyField("http"))
	add("socks5", m.proxyField("socks"))
	return out
}

func (m *model) proxyField(key string) string {
	if m.status == nil {
		return ""
	}
	return stringField(m.status.Proxy, key)
}

// clientHost turns a listen address into something a client can dial: a
// wildcard bind is reported as loopback.
func clientHost(listen string) (string, string) {
	listen = strings.TrimSpace(listen)
	if listen == "" {
		return "", ""
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		if allDigits(listen) {
			return "127.0.0.1", listen
		}
		return "", ""
	}
	switch host {
	case "", "0.0.0.0", "::", "[::]":
		host = "127.0.0.1"
	}
	if host == "" || port == "" {
		return "", ""
	}
	return host, port
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func boolField(m map[string]any, key string) bool {
	if m == nil {
		return false
	}
	if v, ok := m[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return false
}

// listenLine renders "地址（已启用/已停用）" plus any extra flags.
func listenLine(cfg map[string]any, key, onKey string, extra ...string) string {
	addr := stringField(cfg, key)
	if addr == "" {
		return "未设置"
	}
	state := "已停用"
	if boolField(cfg, onKey) {
		state = "已启用"
	}
	suffix := ""
	for _, k := range extra {
		if boolField(cfg, k) {
			suffix += "，UDP"
			break
		}
	}
	return fmt.Sprintf("%s（%s%s）", addr, state, suffix)
}

func enabledLabel(on bool) string {
	if on {
		return "已启用"
	}
	return "已停用"
}

func onOff(on bool) string {
	if on {
		return "开"
	}
	return "关"
}

func onlineLabel(n int) string {
	if n <= 0 {
		return "-"
	}
	return fmt.Sprint(n)
}

func onlineDetail(u engine.UserStatus) string {
	if u.Online == 0 {
		return "暂无在线连接"
	}
	ips := u.OnlineIPs
	if len(ips) > 4 {
		ips = append(append([]string(nil), ips[:4]...), "…")
	}
	return fmt.Sprintf("%d 个 IP：%s", u.Online, strings.Join(ips, ", "))
}

func usersLabel(names []string) string {
	if len(names) == 0 {
		return "-"
	}
	return strings.Join(names, ",")
}

func modeLabel(mode string) string {
	switch mode {
	case config.ModeAuto:
		return "自动测速"
	case config.ModeFixed:
		return "固定节点"
	case config.ModePriority, "":
		return "按优先级"
	default:
		return mode
	}
}

func fallbackLabel(value string) string {
	switch value {
	case config.FallbackDirect:
		return "直连"
	case config.FallbackReject:
		return "拒绝连接"
	case config.FallbackInherit, "":
		return "继承全局"
	default:
		return value
	}
}

func subStatusLabel(s engine.SubStatus) string {
	switch s.LastStatus {
	case "ok":
		return "正常"
	case "error":
		return "拉取失败"
	case "never", "":
		return "未更新"
	default:
		return s.LastStatus
	}
}

// nodeState mirrors the CLI's wording for one node.
func nodeState(n engine.NodeStatus) (string, string) {
	if n.Unsupported != "" {
		return "不支持：" + n.Unsupported, "-"
	}
	if !n.Health.Checked {
		return "未测试", "-"
	}
	if !n.Health.Alive {
		return "不可用", "-"
	}
	return "正常", fmt.Sprintf("%d ms", n.Health.Latency)
}

func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%d 秒", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%d 小时 %d 分", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%d 天 %d 小时", int(d.Hours())/24, int(d.Hours())%24)
}

func relativeTime(t time.Time) string {
	if t.IsZero() {
		return "从未"
	}
	d := time.Since(t)
	switch {
	case d < 0:
		return t.Format("01-02 15:04")
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	default:
		return t.Format("01-02 15:04")
	}
}

func boolStyle(ok bool, yes, no lipgloss.Style) lipgloss.Style {
	if ok {
		return yes
	}
	return no
}
