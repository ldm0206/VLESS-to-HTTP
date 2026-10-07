package engine

import (
	"sort"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/health"
	"github.com/ldm0206/vless-to-http/internal/node"
)

// Status is the snapshot the panel, the TUI and the CLI render.
type Status struct {
	Running   bool      `json:"running"`
	StartedAt time.Time `json:"started_at"`
	UptimeSec int64     `json:"uptime_sec"`
	Restarts  int       `json:"restarts"`
	Revision  uint64    `json:"revision"`
	LastError string    `json:"last_error,omitempty"`
	ProbeAddr string    `json:"probe_addr,omitempty"`
	Dropped   uint64    `json:"dropped_logs"`

	Totals config.Traffic `json:"totals"`
	Users  []UserStatus   `json:"users"`
	Subs   []SubStatus    `json:"subs"`

	NodeSummary NodeSummary `json:"node_summary"`
}

// NodeSummary counts nodes by state for the dashboard cards.
type NodeSummary struct {
	Total       int `json:"total"`
	Usable      int `json:"usable"`
	Unsupported int `json:"unsupported"`
	Alive       int `json:"alive"`
	Dead        int `json:"dead"`
	InUse       int `json:"in_use"`
}

// UserStatus is one account as the panels show it.
type UserStatus struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Mode     string `json:"mode"`
	Fallback string `json:"fallback"`
	Password string `json:"password,omitempty"`
	Note     string `json:"note,omitempty"`
	// Targets is the resolved form used for display and status.
	Targets []config.ResolvedTarget `json:"targets"`
	// RawTargets is what the editor round-trips, so a whole-subscription
	// target stays one row instead of expanding into every node.
	RawTargets   []config.Target `json:"raw_targets"`
	Missing      []string        `json:"missing,omitempty"`
	ActiveNodeID string          `json:"active_node_id,omitempty"`
	ActiveNode   string          `json:"active_node,omitempty"`
	Balancer     bool            `json:"balancer"`
	NodeCount    int             `json:"node_count"`
	Reason       string          `json:"reason,omitempty"`
	Traffic      config.Traffic  `json:"traffic"`
	OnlineIPs    []string        `json:"online_ips,omitempty"`
	Online       int             `json:"online"`
}

// SubStatus is one subscription with its fetch outcome.
type SubStatus struct {
	config.Subscription
	CachedNodes  int    `json:"cached_nodes"`
	CachedUsable int    `json:"cached_usable"`
	Error        string `json:"error,omitempty"`
}

// NodeStatus is one node plus its health and usage.
type NodeStatus struct {
	node.Node
	SubName string       `json:"sub_name"`
	Health  health.State `json:"health"`
	UsedBy  []string     `json:"used_by,omitempty"`
	InUse   bool         `json:"in_use"`
}

// Status assembles the current snapshot.
func (e *Engine) Status() Status {
	cfg := e.store.Get()
	st := Status{
		Revision:  cfg.Revision,
		ProbeAddr: e.probeAddr,
		Totals:    config.Traffic{},
		Users:     []UserStatus{},
		Subs:      []SubStatus{},
	}

	e.mu.RLock()
	res := e.current
	inst := e.inst
	st.Running = inst != nil
	st.StartedAt = e.startedAt
	st.Restarts = e.restarts
	st.LastError = e.lastErr
	e.mu.RUnlock()

	if !st.StartedAt.IsZero() {
		st.UptimeSec = int64(time.Since(st.StartedAt).Seconds())
	}
	if e.logger != nil {
		st.Dropped = e.logger.Dropped()
	}

	traffic := e.Traffic()
	online := e.OnlineIPs()

	for i := range cfg.Users {
		u := &cfg.Users[i]
		us := UserStatus{
			ID:         u.ID,
			Name:       u.Name,
			Enabled:    u.Enabled,
			Mode:       u.Mode,
			Fallback:   cfg.EffectiveFallback(u),
			Note:       u.Note,
			Traffic:    traffic[u.ID],
			RawTargets: u.Targets,
		}
		if route := routeOf(res, u.ID); route != nil {
			us.Targets = route.Targets
			us.Missing = route.Missing
			us.ActiveNodeID = route.ActiveNode
			us.Balancer = route.Balancer != ""
			us.NodeCount = len(route.NodeIDs)
			us.Reason = route.Reason
			if route.ActiveNode != "" {
				us.ActiveNode = e.nodeName(route.ActiveNode)
			}
		}
		if ips, ok := online[u.Name]; ok {
			sort.Strings(ips)
			us.OnlineIPs = ips
			us.Online = len(ips)
		}
		st.Totals.Up += us.Traffic.Up
		st.Totals.Down += us.Traffic.Down
		st.Users = append(st.Users, us)
	}

	for i := range cfg.Subscriptions {
		sub := cfg.Subscriptions[i]
		cached := e.cache.Nodes(sub.ID)
		ss := SubStatus{Subscription: sub, CachedNodes: len(cached)}
		for j := range cached {
			if cached[j].Unsupported == "" {
				ss.CachedUsable++
			}
		}
		if sub.LastError != "" {
			ss.Error = sub.LastError
		}
		st.Subs = append(st.Subs, ss)
	}

	var healthSnapshot map[string]health.State
	if e.prober != nil {
		healthSnapshot = e.prober.Snapshot()
	}
	broken := e.brokenNodes()
	for i := range cfg.Subscriptions {
		for _, n := range e.cache.Nodes(cfg.Subscriptions[i].ID) {
			st.NodeSummary.Total++
			if n.Unsupported != "" || broken[n.ID] != "" {
				st.NodeSummary.Unsupported++
				continue
			}
			st.NodeSummary.Usable++
			if state, ok := healthSnapshot[n.ID]; ok {
				if state.Alive {
					st.NodeSummary.Alive++
				} else if state.Checked {
					st.NodeSummary.Dead++
				}
			}
		}
	}
	if res != nil {
		st.NodeSummary.InUse = len(res.UsedNodes)
	}

	return st
}

// Nodes lists every cached node with health and usage, for the node table.
func (e *Engine) Nodes() []NodeStatus {
	cfg := e.store.Get()
	e.mu.RLock()
	res := e.current
	e.mu.RUnlock()

	usedBy := e.nodeUsage(res)
	var healthSnapshot map[string]health.State
	if e.prober != nil {
		healthSnapshot = e.prober.Snapshot()
	}

	broken := e.brokenNodes()

	out := []NodeStatus{}
	for i := range cfg.Subscriptions {
		sub := cfg.Subscriptions[i]
		for _, n := range e.cache.Nodes(sub.ID) {
			// The cache stores nodes as parsed; their subscription is only
			// implied by which list they came from, so it is stamped here.
			n.SubID = sub.ID
			status := NodeStatus{
				Node:    n,
				SubName: sub.Name,
				Health:  healthSnapshot[n.ID],
				UsedBy:  usedBy[n.ID],
				InUse:   len(usedBy[n.ID]) > 0,
			}
			if !status.Health.Checked {
				status.Health.Alive = n.Unsupported == ""
			}
			// A node the core rejected is unusable for the same reason an
			// unsupported protocol is, so it is reported the same way.
			if reason, rejected := broken[n.ID]; rejected && status.Unsupported == "" {
				status.Unsupported = "内核拒绝了该节点配置：" + reason
			}
			out = append(out, status)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SubName != out[j].SubName {
			return out[i].SubName < out[j].SubName
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// nodeUsage maps node id to the accounts that can reach it.
func (e *Engine) nodeUsage(res *resolution) map[string][]string {
	out := map[string][]string{}
	if res == nil {
		return out
	}
	for _, route := range res.Routes {
		for _, id := range route.NodeIDs {
			out[id] = append(out[id], route.UserName)
		}
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}

func routeOf(res *resolution, userID string) *UserRoute {
	if res == nil {
		return nil
	}
	return res.Routes[userID]
}

// UserRouteOf returns the compiled route of one user, for the API.
func (e *Engine) UserRouteOf(userID string) *UserRoute {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return routeOf(e.current, userID)
}
