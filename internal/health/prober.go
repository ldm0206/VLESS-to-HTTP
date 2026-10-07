// Package health measures whether a node is actually usable by pushing a real
// request through it, and remembers the result for failover decisions.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	"golang.org/x/net/proxy"

	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// Options configures the probe loop.
type Options struct {
	Enabled   bool
	Interval  time.Duration
	Timeout   time.Duration
	Failures  int
	Successes int
	ProbeURL  string
	// Concurrency bounds how many probes run at once.
	Concurrency int
}

// State is the health of one node.
type State struct {
	NodeID    string    `json:"node_id"`
	Alive     bool      `json:"alive"`
	Latency   int64     `json:"latency_ms"`
	Failures  int       `json:"failures"`
	Successes int       `json:"successes"`
	LastCheck time.Time `json:"last_check"`
	LastError string    `json:"last_error,omitempty"`
	// Checked is false until the first probe finishes, so the engine can treat
	// unknown nodes as usable instead of failing everything at boot.
	Checked bool `json:"checked"`
}

// Prober runs periodic probes through the core's loopback probe inbound.
type Prober struct {
	mu     sync.RWMutex
	opts   Options
	states map[string]*State
	logger *logs.Logger

	endpointMu sync.RWMutex
	endpoint   string
	password   string

	// onChange fires when a node flips between alive and dead.
	onChange func()

	clientMu sync.Mutex
	clients  map[string]*http.Client
	clientEP string
}

// New creates a prober; nothing runs until Run is called.
func New(opts Options, logger *logs.Logger) *Prober {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	if opts.Failures <= 0 {
		opts.Failures = 2
	}
	if opts.Successes <= 0 {
		opts.Successes = 1
	}
	return &Prober{
		opts:    opts,
		states:  map[string]*State{},
		clients: map[string]*http.Client{},
		logger:  logger,
	}
}

// SetEndpoint tells the prober where the core's probe inbound listens.
func (p *Prober) SetEndpoint(addr, password string) {
	p.endpointMu.Lock()
	p.endpoint = addr
	p.password = password
	p.endpointMu.Unlock()
}

// SetOptions swaps the tuning between runs.
func (p *Prober) SetOptions(opts Options) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 8
	}
	p.mu.Lock()
	p.opts = opts
	p.mu.Unlock()
}

// OnChange registers the callback fired on every alive/dead transition.
func (p *Prober) OnChange(fn func()) { p.onChange = fn }

// Track makes sure every listed node has a state entry and drops the rest, so
// the map cannot grow without bound as subscriptions change.
func (p *Prober) Track(nodeIDs []string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	keep := make(map[string]bool, len(nodeIDs))
	for _, id := range nodeIDs {
		keep[id] = true
		if _, ok := p.states[id]; !ok {
			// Unprobed nodes start optimistic: a fresh node should be tried
			// before being written off.
			p.states[id] = &State{NodeID: id, Alive: true}
		}
	}
	for id := range p.states {
		if !keep[id] {
			delete(p.states, id)
		}
	}
}

// Snapshot copies the current health of every tracked node.
func (p *Prober) Snapshot() map[string]State {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]State, len(p.states))
	for id, st := range p.states {
		out[id] = *st
	}
	return out
}

// Get returns one node's health.
func (p *Prober) Get(nodeID string) (State, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, ok := p.states[nodeID]
	if !ok {
		return State{}, false
	}
	return *st, true
}

// Healthy reports whether a node should be used, treating never-probed nodes
// as usable.
func (p *Prober) Healthy(nodeID string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	st, ok := p.states[nodeID]
	if !ok {
		return true
	}
	return st.Alive
}

// Run probes every tracked node until ctx is cancelled.
func (p *Prober) Run(ctx context.Context) {
	p.mu.RLock()
	interval := p.opts.Interval
	enabled := p.opts.Enabled
	p.mu.RUnlock()
	if !enabled {
		return
	}
	if interval <= 0 {
		interval = 30 * time.Second
	}

	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p.probeAll(ctx)
			p.mu.RLock()
			interval = p.opts.Interval
			p.mu.RUnlock()
			if interval <= 0 {
				interval = 30 * time.Second
			}
			timer.Reset(interval)
		}
	}
}

func (p *Prober) probeAll(ctx context.Context) {
	p.mu.RLock()
	ids := make([]string, 0, len(p.states))
	for id := range p.states {
		ids = append(ids, id)
	}
	limit := p.opts.Concurrency
	p.mu.RUnlock()
	if len(ids) == 0 {
		return
	}

	sem := make(chan struct{}, limit)
	var wg sync.WaitGroup
	for _, id := range ids {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		default:
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			defer func() { <-sem }()
			p.probeOne(ctx, nodeID)
		}(id)
	}
	wg.Wait()
}

func (p *Prober) probeOne(ctx context.Context, nodeID string) {
	timeout := p.timeout()
	probeCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	err := p.request(probeCtx, nodeID)
	latency := time.Since(start)
	p.record(nodeID, err, latency)
}

// Test runs a single probe right now, for the panel's latency button.
func (p *Prober) Test(ctx context.Context, nodeID string) (State, error) {
	probeCtx, cancel := context.WithTimeout(ctx, p.timeout())
	defer cancel()

	start := time.Now()
	err := p.request(probeCtx, nodeID)
	p.record(nodeID, err, time.Since(start))

	st, _ := p.Get(nodeID)
	return st, err
}

func (p *Prober) record(nodeID string, err error, latency time.Duration) {
	p.mu.Lock()
	st, ok := p.states[nodeID]
	if !ok {
		st = &State{NodeID: nodeID, Alive: true}
		p.states[nodeID] = st
	}
	before := st.Alive
	st.Checked = true
	st.LastCheck = time.Now()

	if err != nil {
		st.Failures++
		st.Successes = 0
		st.LastError = err.Error()
		if st.Failures >= p.opts.Failures {
			st.Alive = false
		}
	} else {
		st.Successes++
		st.Failures = 0
		st.LastError = ""
		st.Latency = latency.Milliseconds()
		if st.Successes >= p.opts.Successes {
			st.Alive = true
		}
	}
	after := st.Alive
	p.mu.Unlock()

	if before != after {
		if p.logger != nil {
			state := "恢复"
			if !after {
				state = "不可用"
			}
			p.logger.Warnf("节点健康检查：%s %s", nodeID, state)
		}
		if p.onChange != nil {
			p.onChange()
		}
	}
}

func (p *Prober) timeout() time.Duration {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.opts.Timeout <= 0 {
		return 5 * time.Second
	}
	return p.opts.Timeout
}

// request pushes one HTTP request through the core's probe inbound to the
// public probe URL that is routed to the node under test.
func (p *Prober) request(ctx context.Context, nodeID string) error {
	client, err := p.httpClient(nodeID)
	if err != nil {
		return err
	}

	p.mu.RLock()
	url := p.opts.ProbeURL
	p.mu.RUnlock()
	if url == "" {
		return errors.New("未配置测速地址")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "v2h-healthcheck")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))

	// Any HTTP answer proves the tunnel worked; only 5xx is treated as a
	// server-side failure.
	if resp.StatusCode >= 500 {
		return fmt.Errorf("测速地址返回 %d", resp.StatusCode)
	}
	return nil
}

// httpClient returns a client whose SOCKS credentials select the node to test
// inside the core, cached per node and rebuilt when the endpoint changes.
func (p *Prober) httpClient(nodeID string) (*http.Client, error) {
	p.endpointMu.RLock()
	addr := p.endpoint
	pass := p.password
	p.endpointMu.RUnlock()
	if addr == "" {
		return nil, errors.New("健康检查尚未启用")
	}

	p.clientMu.Lock()
	defer p.clientMu.Unlock()
	if p.clientEP != addr {
		p.clients = map[string]*http.Client{}
		p.clientEP = addr
	}
	if c, ok := p.clients[nodeID]; ok {
		return c, nil
	}

	dialer, err := p.dialer(addr, xraycore.ProbeUser(nodeID), pass)
	if err != nil {
		return nil, err
	}
	c := &http.Client{
		Transport: &http.Transport{
			DialContext:         dialer.DialContext,
			DisableKeepAlives:   true,
			MaxIdleConnsPerHost: 1,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	p.clients[nodeID] = c
	return c, nil
}

func (p *Prober) dialer(addr, user, pass string) (proxy.ContextDialer, error) {
	d, err := proxy.SOCKS5("tcp", addr, &proxy.Auth{User: user, Password: pass}, proxy.Direct)
	if err != nil {
		return nil, err
	}
	ctxDialer, ok := d.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS 拨号器不支持上下文")
	}
	return ctxDialer, nil
}

// Dialer returns a SOCKS5 dialer bound to one probe account, used by the
// panel's per-node latency test.
func (p *Prober) Dialer(nodeID string) (proxy.ContextDialer, error) {
	p.endpointMu.RLock()
	addr := p.endpoint
	pass := p.password
	p.endpointMu.RUnlock()
	if addr == "" {
		return nil, errors.New("健康检查尚未启用")
	}
	return p.dialer(addr, xraycore.ProbeUser(nodeID), pass)
}

// TCPLatency measures a bare TCP connect, used for nodes that have no probe
// account in the running config.
func TCPLatency(ctx context.Context, host string, port int, timeout time.Duration) (time.Duration, error) {
	d := net.Dialer{Timeout: timeout}
	start := time.Now()
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(port)))
	if err != nil {
		return 0, err
	}
	conn.Close()
	return time.Since(start), nil
}

// SortStates orders health entries by node id for stable output.
func SortStates(states []State) {
	sort.Slice(states, func(i, j int) bool { return states[i].NodeID < states[j].NodeID })
}
