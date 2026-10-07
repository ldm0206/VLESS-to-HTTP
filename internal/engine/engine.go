// Package engine ties the config, the subscriptions and the Xray core
// together: it compiles state into a core config, keeps the core running and
// applies changes when the state moves.
package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ldm0206/vless-to-http/internal/certpin"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/subscription"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// Engine owns the running core and everything derived from the config.
type Engine struct {
	store  *config.Store
	cache  *subscription.Cache
	logger *logs.Logger
	prober *health.Prober
	pins   *certpin.Store

	dataDir   string
	probeAddr string
	probePass string

	mu        sync.RWMutex
	inst      *xraycore.Instance
	current   *resolution
	planJSON  []byte
	startedAt time.Time
	restarts  int
	lastErr   string
	applied   time.Time

	pendingReason string
	pendingTimer  *time.Timer

	// attribution of access log lines
	attrMu  sync.RWMutex
	ipUser  map[string]ipOwner
	tagUser map[string][]string
	online  map[string][]string

	// accumulated traffic, keyed by user id
	trafficMu sync.Mutex
	totals    map[string]config.Traffic
	lastRaw   map[string]xraycore.Traffic
	dirty     bool

	// broken records nodes the core refused to start with, so one malformed
	// server in a subscription cannot take the proxy down for everyone.
	brokenMu sync.RWMutex
	broken   map[string]string

	applyMu  sync.Mutex
	switchMu sync.Mutex

	// pinning guards against stacking probes for the same node.
	pinning inflight
	// stopped is set once the core is shut down. Certificate inspections run
	// outside the wait group, so they have to check it before applying.
	stopped atomic.Bool

	wg sync.WaitGroup
}

type ipOwner struct {
	user string
	seen time.Time
}

// New builds an engine around an already loaded store.
func New(store *config.Store, cache *subscription.Cache, logger *logs.Logger, dataDir string) *Engine {
	cfg := store.Get()

	// The health-check inbound needs a loopback port before the first config
	// is built, so it is reserved here rather than in Run.
	probeAddr, _ := pickFreeAddr()
	e := &Engine{
		store:     store,
		cache:     cache,
		logger:    logger,
		dataDir:   dataDir,
		probeAddr: probeAddr,
		probePass: probePassword(cfg.Panel.APIToken),
		prober:    health.New(healthOptions(cfg), logger),
		pins:      certpin.NewStore(dataDir),
		ipUser:    map[string]ipOwner{},
		tagUser:   map[string][]string{},
		online:    map[string][]string{},
		totals:    map[string]config.Traffic{},
		lastRaw:   map[string]xraycore.Traffic{},
		broken:    map[string]string{},
	}
	e.prober.SetEndpoint(e.probeAddr, e.probePass)
	// The pins are read here rather than in Run: a caller is allowed to Apply a
	// plan before Run, and a plan built without them would drop the pin of
	// every node that needs one.
	if err := e.pins.Load(); err != nil {
		logger.Warnf("读取证书指纹缓存失败：%v", err)
	}
	// Health changes are intrinsic to the engine, so the callback is wired
	// here rather than in Run: an engine driven directly (tests, embedding)
	// must react to node failures too.
	e.prober.OnChange(func() { e.scheduleApply("节点健康状态变化", false) })
	return e
}

// Prober exposes the health prober for the API layer.
func (e *Engine) Prober() *health.Prober { return e.prober }

// Logger exposes the log sink.
func (e *Engine) Logger() *logs.Logger { return e.logger }

// Cache exposes the subscription cache.
func (e *Engine) Cache() *subscription.Cache { return e.cache }

// Store exposes the config store.
func (e *Engine) Store() *config.Store { return e.store }

// Run starts the core and every background loop, and blocks until ctx ends.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.loadTraffic(); err != nil {
		e.logger.Warnf("读取流量统计失败：%v", err)
	}

	if err := e.Apply("启动"); err != nil {
		e.logger.Errorf("启动内核失败：%v", err)
	}

	e.store.OnChange(func(*config.Config) { e.scheduleApply("配置变更", true) })
	e.store.Watch(2*time.Second, func(*config.Config) {
		e.logger.Infof("检测到配置文件被外部修改，正在重新加载")
		e.scheduleApply("配置文件变更", true)
	}, func(err error) {
		e.logger.Warnf("%v（已忽略这次外部修改）", err)
	})

	e.wg.Add(3)
	go func() { defer e.wg.Done(); e.prober.Run(ctx) }()
	go func() { defer e.wg.Done(); e.pollLoop(ctx) }()
	go func() { defer e.wg.Done(); e.trafficLoop(ctx) }()

	if err := e.refreshLoop(ctx); err != nil {
		return err
	}

	e.shutdown()
	return nil
}

// Close stops the core and flushes the accumulated traffic. It is also the
// shutdown path Run takes when its context ends.
func (e *Engine) Close() { e.shutdown() }

func (e *Engine) shutdown() {
	e.wg.Wait()
	e.stopped.Store(true)
	e.applyMu.Lock()
	if e.inst != nil {
		e.inst.Close()
		e.inst = nil
	}
	e.applyMu.Unlock()
	if err := e.saveTraffic(); err != nil {
		e.logger.Warnf("保存流量统计失败：%v", err)
	}
	e.logger.Infof("已停止")
}

// Apply recompiles the config and restarts the core when the result differs
// from what is running.
func (e *Engine) Apply(reason string) error {
	e.applyMu.Lock()
	defer e.applyMu.Unlock()

	cfg := e.store.Get()
	previousJSON := e.planJSON

	res := resolve(cfg, e.nodesWithPins(), e.prober.Healthy, e.brokenNodes(), e.probeAddr, e.probePass)
	// Nodes whose certificate decision is missing or stale are inspected in the
	// background; their pin, if any, lands in the plan on the next apply.
	e.ensurePins(res.UsedNodes)
	raw, err := xraycore.BuildJSON(res.Plan)
	if err != nil {
		e.setError(fmt.Errorf("生成内核配置失败：%w", err))
		return err
	}
	if previousJSON != nil && bytes.Equal(raw, previousJSON) {
		e.mu.Lock()
		e.current = res
		e.tagUser = res.TagUsers
		e.mu.Unlock()
		e.prober.Track(res.ProbeNodes)
		e.prober.SetOptions(healthOptions(cfg))
		return nil
	}

	if e.inst != nil {
		e.inst.Close()
		e.inst = nil
	}

	inst, res, raw, err := e.startWithRetry(cfg, res, raw)

	if err != nil {
		// Try to bring the previous configuration back so the proxy keeps
		// serving while the operator fixes the new one.
		if previousJSON != nil {
			if restored, rerr := xraycore.Start(previousJSON, e.onXrayLog); rerr == nil {
				e.mu.Lock()
				e.inst = restored
				e.mu.Unlock()
			}
		}
		e.setError(fmt.Errorf("内核启动失败：%w", err))
		return err
	}

	e.mu.Lock()
	e.inst = inst
	e.planJSON = raw
	e.current = res
	e.tagUser = res.TagUsers
	e.startedAt = time.Now()
	e.restarts++
	e.lastErr = ""
	e.applied = time.Now()
	e.mu.Unlock()

	e.prober.SetOptions(healthOptions(cfg))
	e.prober.Track(res.ProbeNodes)
	// Nodes that disappeared from the subscriptions leave the rejection list,
	// which keeps it from growing across subscription updates.
	e.forgetMissingBroken()
	e.forgetMissingPins()

	if broken := e.brokenNodes(); len(broken) > 0 {
		for id, reason := range broken {
			e.logger.Warnf("节点 %s 被内核拒绝，已跳过：%s", e.nodeName(id), reason)
		}
	}

	e.logger.Infof("内核已应用新配置（%s）：接入 %d 个节点，%d 个账号",
		reason, len(res.UsedNodes), len(res.Plan.Users))
	for _, route := range res.Routes {
		if route.ActiveNode != "" || route.Reason != "" {
			e.logger.Debugf("账号 %s：%s", route.UserName, e.describeRoute(route))
		}
	}
	return nil
}

func (e *Engine) describeRoute(route *UserRoute) string {
	switch {
	case route.Balancer != "":
		return fmt.Sprintf("自动选择（%d 个节点，内核测速）", len(route.NodeIDs))
	case route.ActiveNode != "":
		return "使用节点 " + e.nodeName(route.ActiveNode) + "，" + route.Reason
	case route.Reason != "":
		return route.Reason
	default:
		return "未配置目标"
	}
}

func (e *Engine) nodeName(id string) string {
	e.mu.RLock()
	res := e.current
	e.mu.RUnlock()
	if res != nil {
		for _, n := range res.UsedNodes {
			if n.ID == id {
				return n.Label()
			}
		}
	}
	return id
}

// scheduleApply coalesces bursts of change events into a single restart.
// Health-driven switches wait out the cooldown so a flapping node cannot
// restart the core over and over; operator changes apply immediately.
func (e *Engine) scheduleApply(reason string, urgent bool) {
	e.switchMu.Lock()
	e.pendingReason = reason
	if e.pendingTimer != nil {
		if urgent {
			e.pendingTimer.Stop()
			e.pendingTimer.Reset(0)
		}
		e.switchMu.Unlock()
		return
	}

	wait := time.Duration(0)
	if !urgent {
		e.mu.RLock()
		cooldown := e.store.Get().Health.SwitchCooldown.D()
		last := e.applied
		e.mu.RUnlock()
		if cooldown <= 0 {
			cooldown = 15 * time.Second
		}
		if remaining := cooldown - time.Since(last); remaining > 0 {
			wait = remaining
		}
	}

	e.pendingTimer = time.AfterFunc(wait, e.runPending)
	e.switchMu.Unlock()
}

func (e *Engine) runPending() {
	e.switchMu.Lock()
	reason := e.pendingReason
	e.pendingTimer = nil
	e.switchMu.Unlock()

	if err := e.Apply(reason); err != nil {
		e.logger.Errorf("应用配置失败：%v", err)
	}
}

func (e *Engine) setError(err error) {
	e.mu.Lock()
	e.lastErr = err.Error()
	e.mu.Unlock()
	e.logger.Errorf("%v", err)
}

// onXrayLog turns core messages into log entries, attributing access lines to
// an account where possible.
func (e *Engine) onXrayLog(rec xraycore.LogRecord) {
	if rec.Access {
		user := e.attribute(rec)
		e.logger.Access(user, "%s", rec.Msg)
		return
	}
	e.logger.FromXray(logs.ParseLevel(rec.Severity), rec.Msg)
}

// attribute guesses which account a connection belongs to: first by client IP
// (Xray keeps that only while a connection is open, so we keep a short sticky
// cache), then by the outbound tag the connection used.
func (e *Engine) attribute(rec xraycore.LogRecord) string {
	if ip := ipOf(rec.From); ip != "" {
		e.attrMu.RLock()
		owner, ok := e.ipUser[ip]
		e.attrMu.RUnlock()
		if ok && time.Since(owner.seen) < 15*time.Minute {
			return owner.user
		}
	}
	if tag := detourTag(rec.Msg); tag != "" {
		e.mu.RLock()
		users := e.tagUser[tag]
		e.mu.RUnlock()
		if len(users) == 1 {
			return users[0]
		}
	}
	return ""
}

// TestNodes probes the given nodes right now; used by the panel and the CLI.
func (e *Engine) TestNodes(ctx context.Context, nodeIDs []string) map[string]health.State {
	out := make(map[string]health.State, len(nodeIDs))
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)

	e.mu.RLock()
	res := e.current
	e.mu.RUnlock()

	probeable := map[string]bool{}
	if res != nil {
		for _, id := range res.ProbeNodes {
			probeable[id] = true
		}
	}

	for _, id := range nodeIDs {
		wg.Add(1)
		go func(nodeID string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if !probeable[nodeID] {
				// No probe account for this node: fall back to a TCP connect
				// so the operator still learns whether the host is reachable.
				state := health.State{NodeID: nodeID, Checked: true, LastCheck: time.Now()}
				if n := e.findNode(nodeID); n != nil {
					if d, err := health.TCPLatency(ctx, n.Server, n.Port, 5*time.Second); err == nil {
						state.Alive = true
						state.Latency = d.Milliseconds()
					} else {
						state.LastError = "TCP 连接失败：" + err.Error()
					}
				} else {
					state.LastError = "节点不存在"
				}
				mu.Lock()
				out[nodeID] = state
				mu.Unlock()
				return
			}

			state, err := e.prober.Test(ctx, nodeID)
			if err != nil {
				state.LastError = err.Error()
			}
			mu.Lock()
			out[nodeID] = state
			mu.Unlock()
		}(id)
	}
	wg.Wait()
	return out
}

func (e *Engine) findNode(nodeID string) *nodeRef {
	for _, nodes := range e.cache.All() {
		for i := range nodes {
			if nodes[i].ID == nodeID {
				return &nodeRef{Server: nodes[i].Server, Port: nodes[i].Port, Label: nodes[i].Label()}
			}
		}
	}
	return nil
}

type nodeRef struct {
	Server string
	Port   int
	Label  string
}

// ForceRestart restarts the core even when nothing changed.
func (e *Engine) ForceRestart() error {
	e.applyMu.Lock()
	e.planJSON = nil
	e.applyMu.Unlock()
	return e.Apply("手动重启")
}

// PlanJSON returns the config the core is currently running.
func (e *Engine) PlanJSON() string {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return string(e.planJSON)
}

func healthOptions(cfg *config.Config) health.Options {
	return health.Options{
		Enabled:     cfg.Health.Enabled,
		Interval:    cfg.Health.Interval.D(),
		Timeout:     cfg.Health.Timeout.D(),
		Failures:    cfg.Health.Failures,
		Successes:   cfg.Health.Successes,
		ProbeURL:    cfg.Health.ProbeURL,
		Concurrency: 8,
	}
}

// pickFreeAddr reserves a loopback port for the health-check inbound.
func pickFreeAddr() (string, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	addr := l.Addr().String()
	l.Close()
	return addr, nil
}

// probePassword derives a secret for the probe accounts from the API token, so
// it is stable across restarts but never appears in the config file.
func probePassword(apiToken string) string {
	if apiToken == "" {
		return "v2h-probe"
	}
	sum := sha256.Sum256([]byte("v2h-probe:" + apiToken))
	return hex.EncodeToString(sum[:])[:24]
}

// ipOf strips the port from an address like "1.2.3.4:5678".
func ipOf(from string) string {
	if from == "" {
		return ""
	}
	host, _, err := net.SplitHostPort(from)
	if err != nil {
		return from
	}
	return host
}

// detourTag extracts the outbound tag from "… [http-in -> node:abc]".
func detourTag(msg string) string {
	start := -1
	for i := 0; i < len(msg); i++ {
		if msg[i] == '[' {
			start = i
			break
		}
	}
	if start < 0 {
		return ""
	}
	end := -1
	for i := start; i < len(msg); i++ {
		if msg[i] == ']' {
			end = i
			break
		}
	}
	if end < 0 {
		return ""
	}
	inner := msg[start+1 : end]
	for i := 0; i+3 <= len(inner); i++ {
		if inner[i:i+3] == "-> " {
			return inner[i+3:]
		}
	}
	return ""
}

var errNoInstance = errors.New("内核未运行")
