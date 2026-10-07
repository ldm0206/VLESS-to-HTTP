package engine

import (
	"fmt"
	"sort"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/node"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// UserRoute is what one user resolved to in the current plan.
type UserRoute struct {
	UserID     string
	UserName   string
	Mode       string
	Fallback   string
	Targets    []config.ResolvedTarget
	Missing    []string
	NodeIDs    []string
	ActiveNode string // node id in use for priority/fixed
	Outbound   string // outbound tag, when not using a balancer
	Balancer   string // balancer tag for auto mode
	Reason     string // why the route looks the way it does
}

// resolution is the compiled view of the config at one point in time.
type resolution struct {
	Plan       xraycore.Plan
	Routes     map[string]*UserRoute // by user id
	ProbeNodes []string
	UsedNodes  []node.Node
	// TagUsers maps an outbound tag to the users whose traffic can use it,
	// which is how access log lines are attributed when the client IP is not
	// in the online map.
	TagUsers map[string][]string
	// NodeSubs maps node id to the subscription it came from.
	NodeSubs map[string]string
}

// resolve compiles the config, the cached subscription nodes and the current
// health into an Xray plan. healthy reports whether a node may be used.
func resolve(cfg *config.Config, subs map[string][]node.Node, healthy func(string) bool, broken map[string]string, probeAddr, probePass string) *resolution {
	res := &resolution{
		Routes:   map[string]*UserRoute{},
		TagUsers: map[string][]string{},
		NodeSubs: map[string]string{},
	}

	index := newSubIndex(cfg, subs)
	used := map[string]bool{}
	var usedNodes []node.Node

	plan := xraycore.Plan{
		HTTP:        xraycore.ListenSpec{Enabled: cfg.Proxy.HTTP.Enabled, Listen: cfg.Proxy.HTTP.Listen},
		SOCKS:       xraycore.ListenSpec{Enabled: cfg.Proxy.SOCKS.Enabled, Listen: cfg.Proxy.SOCKS.Listen},
		SocksUDP:    cfg.Proxy.SOCKS.UDP,
		Sniffing:    cfg.Proxy.Sniffing,
		Timeout:     int(cfg.Proxy.Timeout.D().Seconds()),
		DNSServers:  cfg.Proxy.DNSServers,
		CustomRules: cfg.Proxy.CustomRules,
		ProbeListen: probeAddr,
		ProbePass:   probePass,
		Fallback:    cfg.Proxy.Fallback,
	}

	probeCandidates := map[string]node.Node{}
	balancerNodes := map[string]bool{}

	for i := range cfg.Users {
		u := &cfg.Users[i]
		route := &UserRoute{
			UserID:   u.ID,
			UserName: u.Name,
			Mode:     u.Mode,
			Fallback: cfg.EffectiveFallback(u),
		}
		res.Routes[u.ID] = route

		nodes, targets, missing := index.resolveTargets(u)
		route.Targets = targets
		route.Missing = missing

		usable := make([]node.Node, 0, len(nodes))
		for _, n := range nodes {
			if n.Unsupported != "" {
				continue
			}
			// The core already refused this node once; retrying it would take
			// the whole configuration down again.
			if reason, rejected := broken[n.ID]; rejected {
				route.Missing = append(route.Missing, fmt.Sprintf("%s（内核拒绝：%s）", n.Label(), reason))
				continue
			}
			usable = append(usable, n)
		}

		if u.Enabled {
			for _, n := range usable {
				route.NodeIDs = append(route.NodeIDs, n.ID)
				if !used[n.ID] {
					used[n.ID] = true
					usedNodes = append(usedNodes, n)
					res.NodeSubs[n.ID] = n.SubID
				}
				probeCandidates[n.ID] = n
			}
		}

		if !u.Enabled {
			route.Reason = "账号已停用"
			continue
		}

		fallbackTag := xraycore.DirectTag
		if route.Fallback == config.FallbackReject {
			fallbackTag = xraycore.BlockTag
		}

		switch {
		case len(usable) == 0:
			route.Outbound = fallbackTag
			route.Reason = "没有可用的目标节点，走兜底策略"

		case u.Mode == config.ModeAuto:
			route.Balancer = balancerTag(u.ID)
			for _, n := range usable {
				balancerNodes[n.ID] = true
				res.addTagUser(node.OutboundTag(n.ID), u.Name)
			}

		case u.Mode == config.ModeFixed:
			n := usable[0]
			route.ActiveNode = n.ID
			route.Outbound = node.OutboundTag(n.ID)
			res.addTagUser(route.Outbound, u.Name)
			probeCandidates[n.ID] = n

		default: // priority
			picked := chooseHealthy(usable, healthy)
			if picked == nil {
				route.Outbound = fallbackTag
				route.Reason = "所有目标节点都不可用，走兜底策略"
				break
			}
			route.ActiveNode = picked.ID
			route.Outbound = node.OutboundTag(picked.ID)
			route.Reason = fmt.Sprintf("按顺序选中第 %d 个", indexOf(usable, picked.ID)+1)
			res.addTagUser(route.Outbound, u.Name)
		}

		plan.Users = append(plan.Users, xraycore.PlanUser{
			Name:        u.Name,
			Password:    u.Password,
			OutboundTag: route.Outbound,
			BalancerTag: route.Balancer,
		})
	}

	// Probe accounting: every materialised node gets a probe account so the
	// panel can show a real latency and priority users can fail over.
	probeIDs := make([]string, 0, len(probeCandidates))
	for id := range probeCandidates {
		probeIDs = append(probeIDs, id)
	}
	sort.Strings(probeIDs)
	if cfg.Health.MaxProbes > 0 && len(probeIDs) > cfg.Health.MaxProbes {
		probeIDs = probeIDs[:cfg.Health.MaxProbes]
	}
	for _, id := range probeIDs {
		n := probeCandidates[id]
		plan.Probes = append(plan.Probes, xraycore.PlanProbe{NodeID: id, Name: n.Label()})
		res.ProbeNodes = append(res.ProbeNodes, id)
	}

	// Balancers: one per auto-mode user, over exactly their own nodes.
	for i := range cfg.Users {
		u := &cfg.Users[i]
		route := res.Routes[u.ID]
		if route == nil || route.Balancer == "" {
			continue
		}
		selector := make([]string, 0, len(route.NodeIDs))
		for _, id := range route.NodeIDs {
			selector = append(selector, node.OutboundTag(id))
		}
		fallback := xraycore.DirectTag
		if route.Fallback == config.FallbackReject {
			fallback = xraycore.BlockTag
		}
		plan.Balancers = append(plan.Balancers, xraycore.PlanBalancer{
			Tag:      route.Balancer,
			Selector: selector,
			Fallback: fallback,
		})
	}

	if len(balancerNodes) > 0 {
		interval := cfg.Health.Interval.D()
		if interval <= 0 || interval > 60e9 {
			interval = 60e9
		}
		selector := make([]string, 0, len(balancerNodes))
		for id := range balancerNodes {
			selector = append(selector, node.OutboundTag(id))
		}
		sort.Strings(selector)
		plan.Observatory = &xraycore.Observatory{
			Selector: selector,
			URL:      cfg.Health.ProbeURL,
			Interval: fmt.Sprintf("%ds", int(interval.Seconds())),
			Timeout:  fmt.Sprintf("%ds", maxInt(1, int(cfg.Health.Timeout.D().Seconds()))),
			Sampling: 1,
		}
	}

	xraycore.SortNodes(usedNodes)
	res.UsedNodes = usedNodes
	plan.Nodes = usedNodes
	res.Plan = plan
	return res
}

func (r *resolution) addTagUser(tag, user string) {
	for _, existing := range r.TagUsers[tag] {
		if existing == user {
			return
		}
	}
	r.TagUsers[tag] = append(r.TagUsers[tag], user)
}

// chooseHealthy returns the first healthy node in priority order, treating
// nodes that were never probed as usable.
func chooseHealthy(nodes []node.Node, healthy func(string) bool) *node.Node {
	for i := range nodes {
		if healthy(nodes[i].ID) {
			return &nodes[i]
		}
	}
	return nil
}

func indexOf(nodes []node.Node, id string) int {
	for i := range nodes {
		if nodes[i].ID == id {
			return i
		}
	}
	return -1
}

func balancerTag(userID string) string { return "bal:" + userID }

// subIndex speeds up node lookups by subscription and by name.
type subIndex struct {
	cfg  *config.Config
	subs map[string][]node.Node
	byID map[string]node.Node
}

func newSubIndex(cfg *config.Config, subs map[string][]node.Node) *subIndex {
	idx := &subIndex{cfg: cfg, subs: subs, byID: map[string]node.Node{}}
	for _, nodes := range subs {
		for _, n := range nodes {
			idx.byID[n.ID] = n
		}
	}
	return idx
}

// resolveTargets flattens a user's targets into ordered nodes, reporting the
// ones that no longer exist.
func (s *subIndex) resolveTargets(u *config.User) ([]node.Node, []config.ResolvedTarget, []string) {
	var nodes []node.Node
	var resolved []config.ResolvedTarget
	var missing []string
	seen := map[string]bool{}

	subName := func(id string) string {
		if sub := s.cfg.FindSub(id); sub != nil {
			return sub.Name
		}
		return id
	}

	add := func(n node.Node, subID string) {
		if seen[n.ID] {
			return
		}
		seen[n.ID] = true
		if n.SubID == "" {
			n.SubID = subID
		}
		nodes = append(nodes, n)
		resolved = append(resolved, config.ResolvedTarget{
			Sub:      subID,
			SubName:  subName(subID),
			NodeID:   n.ID,
			NodeName: n.Label(),
			Type:     n.Type,
			Missing:  n.Unsupported != "",
		})
	}

	for _, t := range u.Targets {
		if t.All {
			lists := s.listsFor(t.Sub)
			limit := t.Limit
			count := 0
			for _, entry := range lists {
				for _, n := range entry.nodes {
					if n.Unsupported != "" {
						continue
					}
					if limit > 0 && count >= limit {
						break
					}
					add(n, entry.subID)
					count++
				}
			}
			if count == 0 {
				if t.Sub != "" {
					missing = append(missing, fmt.Sprintf("订阅 %s 没有可用节点", subName(t.Sub)))
				} else {
					missing = append(missing, "没有任何订阅提供可用节点")
				}
			}
			continue
		}

		found := s.find(t.Sub, t.Node)
		if found == nil {
			missing = append(missing, t.Node)
			resolved = append(resolved, config.ResolvedTarget{
				Sub:      t.Sub,
				SubName:  subName(t.Sub),
				NodeName: t.Node,
				Missing:  true,
			})
			continue
		}
		if found.Unsupported != "" {
			missing = append(missing, fmt.Sprintf("%s（%s）", found.Label(), found.Unsupported))
		}
		add(*found, found.SubID)
	}
	return nodes, resolved, missing
}

type subList struct {
	subID string
	nodes []node.Node
}

// listsFor returns the subscriptions to search, in the configured order.
func (s *subIndex) listsFor(subID string) []subList {
	if subID != "" {
		if nodes, ok := s.subs[subID]; ok {
			return []subList{{subID: subID, nodes: nodes}}
		}
		return nil
	}
	out := make([]subList, 0, len(s.cfg.Subscriptions))
	for _, sub := range s.cfg.Subscriptions {
		if nodes, ok := s.subs[sub.ID]; ok {
			out = append(out, subList{subID: sub.ID, nodes: nodes})
		}
	}
	return out
}

// find locates a node by id first (stable across renames) and by name second.
func (s *subIndex) find(subID, key string) *node.Node {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil
	}
	lists := s.listsFor(subID)
	for _, list := range lists {
		for i := range list.nodes {
			if list.nodes[i].ID == key {
				return &list.nodes[i]
			}
		}
	}
	for _, list := range lists {
		for i := range list.nodes {
			if list.nodes[i].Name == key {
				return &list.nodes[i]
			}
		}
	}
	for _, list := range lists {
		for i := range list.nodes {
			if strings.EqualFold(list.nodes[i].Name, key) {
				return &list.nodes[i]
			}
		}
	}
	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
