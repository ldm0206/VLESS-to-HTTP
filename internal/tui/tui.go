// Package tui renders the terminal console. It talks to a running v2h over
// the same HTTP API the web panel uses, so `v2h tui` also works from
// `docker exec` against the container's own instance. The console never
// touches the config file or the Xray core directly.
package tui

import (
	"context"
	"errors"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/ldm0206/vless-to-http/internal/client"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/logs"
)

// Options tells the console where to connect.
type Options struct {
	BaseURL string
	Token   string
}

// Client is the part of *client.Client the console depends on. Keeping it an
// interface lets the tests drive the model without a server running.
type Client interface {
	Base() string
	Status(ctx context.Context) (*client.StatusResponse, error)
	Users(ctx context.Context) ([]engine.UserStatus, error)
	Subs(ctx context.Context) ([]engine.SubStatus, error)
	Nodes(ctx context.Context, subID string) ([]engine.NodeStatus, error)
	TestNodes(ctx context.Context, ids []string) (map[string]health.State, error)
	TestSub(ctx context.Context, subID string) (map[string]health.State, error)
	Logs(ctx context.Context, since uint64, limit int, level, user, query string) ([]logs.Entry, uint64, error)
	RefreshSub(ctx context.Context, id string) (*engine.SubStatus, error)
	RestartCore(ctx context.Context) error
	UpdateUser(ctx context.Context, id string, body map[string]any) (*engine.UserStatus, error)
	ResetUserTraffic(ctx context.Context, id string) error
}

// *client.Client is the production implementation.
var _ Client = (*client.Client)(nil)

// Run starts the console and blocks until the user quits. Quitting the
// console leaves the server running.
func Run(ctx context.Context, opts Options) error {
	m := newModel(ctx, client.New(opts.BaseURL, opts.Token), opts)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	if _, err := p.Run(); err != nil {
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	return nil
}

// --- views ----------------------------------------------------------------

type view int

const (
	viewOverview view = iota
	viewUsers
	viewSubs
	viewNodes
	viewLogs
	viewHelp
	viewCount
)

var viewTitles = [viewCount]string{"概览", "账号", "订阅", "节点", "日志", "帮助"}

// Polling cadence. The TUI polls the panel API; it never opens an SSE
// connection, which keeps `docker exec` sessions cheap.
const (
	statusInterval  = time.Second
	viewInterval    = 2 * time.Second
	logInterval     = 500 * time.Millisecond
	logIdleInterval = 2 * time.Second
	spinnerInterval = 120 * time.Millisecond

	maxLogEntries = 500
	logPageSize   = 200

	defaultWidth  = 100
	defaultHeight = 30
)

type tickKind int

const (
	tickStatus tickKind = iota
	tickView
	tickLogs
	tickSpinner
)

type tickMsg struct{ kind tickKind }

func tickCmd(d time.Duration, kind tickKind) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{kind: kind} })
}

type statusMsg struct {
	resp *client.StatusResponse
	err  error
}

type usersMsg struct {
	users []engine.UserStatus
	err   error
}

type subsMsg struct {
	subs []engine.SubStatus
	err  error
}

type nodesMsg struct {
	nodes []engine.NodeStatus
	err   error
}

type entriesMsg struct {
	entries []logs.Entry
	last    uint64
	err     error
}

// actionMsg reports the outcome of a one-shot action (restart, refresh,
// test, toggle) that does not fit the per-view poll results.
type actionMsg struct {
	done  string // success text, already in Chinese
	err   error
	label string // action being performed, for the failure text
}

type confirmKind int

const (
	confirmRestartCore confirmKind = iota
	confirmResetTraffic
)

// confirmState is the inline y/n prompt shown instead of a separate screen.
type confirmState struct {
	kind   confirmKind
	prompt string
	userID string
}

// --- model ----------------------------------------------------------------

type model struct {
	ctx  context.Context
	api  Client
	opts Options
	ui   styles

	width, height int

	current view
	overlay bool // help overlay

	// Snapshots of the server state.
	status  *client.StatusResponse
	users   []engine.UserStatus
	subs    []engine.SubStatus
	nodes   []engine.NodeStatus
	entries []logs.Entry
	lastSeq uint64

	// Cursors, one per list view.
	userIdx    int
	subIdx     int
	nodeIdx    int
	userDetail bool
	expanded   map[string]bool

	// Log view.
	follow    bool
	levelIdx  int
	query     string
	searching bool
	draft     string
	scroll    int

	// Actions in flight.
	busyAction bool
	actionText string
	spinFrame  int
	notice     string
	noticeBad  bool

	confirm *confirmState

	// Connection state.
	unauthorized bool
	lastErr      string
	nextRetry    time.Time
	failures     int

	// One request per endpoint at a time, so ticks never overlap.
	busyStatus bool
	busyUsers  bool
	busySubs   bool
	busyNodes  bool
	busyLogs   bool
}

func newModel(ctx context.Context, api Client, opts Options) *model {
	return &model{
		ctx:      ctx,
		api:      api,
		opts:     opts,
		ui:       newStyles(os.Getenv("NO_COLOR") != ""),
		current:  viewOverview,
		follow:   true,
		expanded: map[string]bool{},
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(statusInterval, tickStatus),
		tickCmd(viewInterval, tickView),
		tickCmd(logIdleInterval, tickLogs),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil

	case tea.KeyMsg:
		return m, m.handleKey(msg)

	case tickMsg:
		return m, m.onTick(msg.kind)

	case statusMsg:
		m.busyStatus = false
		if msg.err != nil {
			m.noteErr(msg.err)
			return m, nil
		}
		m.noteOK()
		if msg.resp != nil {
			m.status = msg.resp
		}
		return m, nil

	case usersMsg:
		m.busyUsers = false
		if msg.err != nil {
			m.noteErr(msg.err)
			return m, nil
		}
		m.noteOK()
		m.users = msg.users
		m.clampCursors()
		return m, nil

	case subsMsg:
		m.busySubs = false
		if msg.err != nil {
			m.noteErr(msg.err)
			return m, nil
		}
		m.noteOK()
		m.subs = msg.subs
		m.clampCursors()
		return m, nil

	case nodesMsg:
		m.busyNodes = false
		if msg.err != nil {
			m.noteErr(msg.err)
			return m, nil
		}
		m.noteOK()
		m.nodes = msg.nodes
		m.clampCursors()
		return m, nil

	case entriesMsg:
		m.busyLogs = false
		if msg.err != nil {
			m.noteErr(msg.err)
			return m, nil
		}
		m.noteOK()
		m.appendEntries(msg.entries, msg.last)
		return m, nil

	case actionMsg:
		m.busyAction = false
		m.actionText = ""
		if msg.err != nil {
			m.notice = msg.label + "失败：" + msg.err.Error()
			m.noticeBad = true
			m.noteErr(msg.err)
		} else {
			m.notice = msg.done
			m.noticeBad = false
		}
		// Pull fresh numbers so the view reflects the action.
		return m, tea.Batch(m.pollStatus(), m.pollView())
	}
	return m, nil
}

// onTick re-arms the tick that just fired and issues the matching request
// unless one is already in flight.
func (m *model) onTick(kind tickKind) tea.Cmd {
	cmds := make([]tea.Cmd, 0, 2)
	switch kind {
	case tickStatus:
		cmds = append(cmds, tickCmd(statusInterval, tickStatus))
		if cmd := m.pollStatus(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tickView:
		cmds = append(cmds, tickCmd(viewInterval, tickView))
		if cmd := m.pollView(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tickLogs:
		interval := logIdleInterval
		if m.current == viewLogs {
			interval = logInterval
		}
		cmds = append(cmds, tickCmd(interval, tickLogs))
		if cmd := m.pollLogs(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case tickSpinner:
		if m.busyAction {
			m.spinFrame++
			cmds = append(cmds, tickCmd(spinnerInterval, tickSpinner))
		}
	}
	return tea.Batch(cmds...)
}

// --- polling --------------------------------------------------------------

// polling reports whether requests may go out right now. An invalid token
// stops the loop until the user retries; connection failures back off.
func (m *model) polling() bool {
	if m.unauthorized {
		return false
	}
	if !m.nextRetry.IsZero() && time.Now().Before(m.nextRetry) {
		return false
	}
	return true
}

func (m *model) pollStatus() tea.Cmd {
	if m.busyStatus || !m.polling() {
		return nil
	}
	m.busyStatus = true
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		resp, err := api.Status(ctx)
		return statusMsg{resp: resp, err: err}
	}
}

// pollView fetches whatever the active view renders. The subscription view
// also needs the node list, since an expanded row lists that subscription's
// nodes.
func (m *model) pollView() tea.Cmd {
	if !m.polling() {
		return nil
	}
	cmds := make([]tea.Cmd, 0, 2)
	switch m.current {
	case viewUsers:
		if cmd := m.pollUsers(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case viewSubs:
		if cmd := m.pollSubs(); cmd != nil {
			cmds = append(cmds, cmd)
		}
		if cmd := m.pollNodes(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	case viewNodes:
		if cmd := m.pollNodes(); cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func (m *model) pollUsers() tea.Cmd {
	if m.busyUsers {
		return nil
	}
	m.busyUsers = true
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		users, err := api.Users(ctx)
		return usersMsg{users: users, err: err}
	}
}

func (m *model) pollSubs() tea.Cmd {
	if m.busySubs {
		return nil
	}
	m.busySubs = true
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		subs, err := api.Subs(ctx)
		return subsMsg{subs: subs, err: err}
	}
}

func (m *model) pollNodes() tea.Cmd {
	if m.busyNodes {
		return nil
	}
	m.busyNodes = true
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		nodes, err := api.Nodes(ctx, "")
		return nodesMsg{nodes: nodes, err: err}
	}
}

func (m *model) pollLogs() tea.Cmd {
	if m.busyLogs || !m.polling() {
		return nil
	}
	m.busyLogs = true
	since, level, query := m.lastSeq, m.logLevel(), m.query
	api, ctx := m.api, m.ctx
	return func() tea.Msg {
		entries, last, err := api.Logs(ctx, since, logPageSize, level, "", query)
		return entriesMsg{entries: entries, last: last, err: err}
	}
}

func (m *model) appendEntries(entries []logs.Entry, last uint64) {
	if len(entries) > 0 {
		m.entries = append(m.entries, entries...)
		if excess := len(m.entries) - maxLogEntries; excess > 0 {
			m.entries = append([]logs.Entry(nil), m.entries[excess:]...)
		}
	}
	if last > m.lastSeq {
		m.lastSeq = last
	}
	m.clampScroll()
}

// resetLogs drops the buffer and re-reads history under the current filter.
func (m *model) resetLogs() {
	m.entries = nil
	m.lastSeq = 0
	m.scroll = 0
}

// --- error handling -------------------------------------------------------

// noteErr records a failed request. A rejected token stops polling until the
// user presses r; connection errors keep the last known data on screen and
// retry with a growing delay.
func (m *model) noteErr(err error) {
	if err == nil {
		m.failures = 0
		m.nextRetry = time.Time{}
		m.lastErr = ""
		return
	}
	if client.IsUnauthorized(err) {
		m.unauthorized = true
		m.lastErr = ""
		return
	}
	m.lastErr = err.Error()

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		// The server answered, so retrying at the normal cadence costs
		// nothing: only a real connection failure backs off.
		m.failures = 0
		m.nextRetry = time.Time{}
		return
	}
	m.failures++
	m.nextRetry = time.Now().Add(retryDelay(m.failures))
}

// noteOK clears the connection trouble after any successful request.
func (m *model) noteOK() {
	m.failures = 0
	m.nextRetry = time.Time{}
	m.lastErr = ""
}

func retryDelay(failures int) time.Duration {
	switch {
	case failures <= 1:
		return time.Second
	case failures == 2:
		return 2 * time.Second
	case failures == 3:
		return 5 * time.Second
	default:
		return 10 * time.Second
	}
}

// retry resumes polling after an invalid token was reported.
func (m *model) retry() tea.Cmd {
	m.unauthorized = false
	m.failures = 0
	m.nextRetry = time.Time{}
	m.lastErr = ""
	m.notice = "正在重新连接…"
	m.noticeBad = false
	return tea.Batch(m.pollStatus(), m.pollView(), m.pollLogs())
}

// startAction marks an action as running so the footer shows a spinner, and
// arms the tick that animates it.
func (m *model) startAction(text string) tea.Cmd {
	m.busyAction = true
	m.actionText = text
	m.spinFrame = 0
	return tickCmd(spinnerInterval, tickSpinner)
}

// --- helpers --------------------------------------------------------------

func (m *model) dims() (int, int) {
	w, h := m.width, m.height
	if w <= 0 {
		w = defaultWidth
	}
	if h <= 0 {
		h = defaultHeight
	}
	return w, h
}

func (m *model) clampCursors() {
	m.userIdx = clamp(m.userIdx, 0, len(m.users)-1)
	m.subIdx = clamp(m.subIdx, 0, len(m.subs)-1)
	m.nodeIdx = clamp(m.nodeIdx, 0, len(m.nodes)-1)
}

func (m *model) clampScroll() {
	max := len(m.entries) - 1
	if max < 0 {
		max = 0
	}
	m.scroll = clamp(m.scroll, 0, max)
}

func (m *model) moveCursor(delta int, length int) {
	if length == 0 {
		return
	}
	switch m.current {
	case viewUsers:
		m.userIdx = clamp(m.userIdx+delta, 0, length-1)
	case viewSubs:
		m.subIdx = clamp(m.subIdx+delta, 0, length-1)
	case viewNodes:
		m.nodeIdx = clamp(m.nodeIdx+delta, 0, length-1)
	case viewLogs:
		if m.follow && delta < 0 {
			m.follow = false
			m.scroll = 0
		}
		if !m.follow {
			m.scroll = clamp(m.scroll-delta, 0, maxInt(0, len(m.entries)-1))
		}
	}
}

func (m *model) selectedUser() *engine.UserStatus {
	if m.userIdx < 0 || m.userIdx >= len(m.users) {
		return nil
	}
	return &m.users[m.userIdx]
}

func (m *model) selectedSub() *engine.SubStatus {
	if m.subIdx < 0 || m.subIdx >= len(m.subs) {
		return nil
	}
	return &m.subs[m.subIdx]
}

func (m *model) selectedNode() *engine.NodeStatus {
	if m.nodeIdx < 0 || m.nodeIdx >= len(m.nodes) {
		return nil
	}
	return &m.nodes[m.nodeIdx]
}

func clamp(v, lo, hi int) int {
	if hi < lo {
		return lo
	}
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
