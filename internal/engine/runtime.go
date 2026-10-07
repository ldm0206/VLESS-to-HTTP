package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/subscription"
)

// pollLoop refreshes traffic counters and the client-IP map once per second.
func (e *Engine) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.pollOnce()
		}
	}
}

func (e *Engine) pollOnce() {
	cfg := e.store.Get()

	names := make([]string, 0, len(cfg.Users))
	nameToID := make(map[string]string, len(cfg.Users))
	for i := range cfg.Users {
		if !cfg.Users[i].Enabled || cfg.Users[i].Password == "" {
			continue
		}
		names = append(names, cfg.Users[i].Name)
		nameToID[cfg.Users[i].Name] = cfg.Users[i].ID
	}
	if len(names) == 0 {
		return
	}

	e.mu.RLock()
	inst := e.inst
	e.mu.RUnlock()
	if inst == nil {
		return
	}

	traffic := inst.UserTraffic(names)
	online := inst.UserOnline(names)
	now := time.Now()

	e.trafficMu.Lock()
	for name, cur := range traffic {
		id, ok := nameToID[name]
		if !ok {
			continue
		}
		last := e.lastRaw[name]
		up := cur.Up - last.Up
		down := cur.Down - last.Down
		// A negative delta means the core restarted and the counters were
		// reset, so the new value is the delta.
		if up < 0 {
			up = cur.Up
		}
		if down < 0 {
			down = cur.Down
		}
		e.lastRaw[name] = cur
		if up == 0 && down == 0 {
			continue
		}
		total := e.totals[id]
		total.Up += up
		total.Down += down
		e.totals[id] = total
		e.dirty = true
	}
	e.trafficMu.Unlock()

	e.attrMu.Lock()
	e.online = online
	for name, ips := range online {
		for _, ip := range ips {
			e.ipUser[ip] = ipOwner{user: name, seen: now}
		}
	}
	for ip, owner := range e.ipUser {
		if now.Sub(owner.seen) > 30*time.Minute {
			delete(e.ipUser, ip)
		}
	}
	e.attrMu.Unlock()
}

// trafficLoop persists accumulated traffic every 30 seconds.
func (e *Engine) trafficLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.trafficMu.Lock()
			dirty := e.dirty
			e.trafficMu.Unlock()
			if !dirty {
				continue
			}
			if err := e.saveTraffic(); err != nil {
				e.logger.Warnf("保存流量统计失败：%v", err)
			}
		}
	}
}

type trafficFile struct {
	Version int                       `json:"version"`
	Saved   time.Time                 `json:"saved"`
	Users   map[string]config.Traffic `json:"users"`
}

func (e *Engine) trafficPath() string {
	return filepath.Join(e.dataDir, "state", "traffic.json")
}

func (e *Engine) loadTraffic() error {
	raw, err := os.ReadFile(e.trafficPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var file trafficFile
	if err := json.Unmarshal(raw, &file); err != nil {
		return err
	}
	if file.Users == nil {
		return nil
	}
	e.trafficMu.Lock()
	e.totals = file.Users
	e.trafficMu.Unlock()
	return nil
}

func (e *Engine) saveTraffic() error {
	e.trafficMu.Lock()
	snapshot := make(map[string]config.Traffic, len(e.totals))
	for id, t := range e.totals {
		snapshot[id] = t
	}
	e.dirty = false
	e.trafficMu.Unlock()

	path := e.trafficPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(trafficFile{Version: 1, Saved: time.Now(), Users: snapshot}, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// refreshLoop keeps subscriptions up to date.
func (e *Engine) refreshLoop(ctx context.Context) error {
	// Nodes that were never fetched are pulled right away, in the background
	// so a slow provider cannot delay the panel.
	go e.refreshMissing(ctx)

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			e.refreshDue(ctx)
		}
	}
}

func (e *Engine) refreshMissing(ctx context.Context) {
	for _, sub := range e.store.Get().Subscriptions {
		if !sub.Enabled || sub.URL == "" || sub.LastStatus == "ok" {
			continue
		}
		if len(e.cache.Nodes(sub.ID)) > 0 {
			continue
		}
		if err := e.RefreshSubscription(ctx, sub.ID); err != nil {
			e.logger.Warnf("订阅「%s」首次拉取失败：%v", sub.Name, err)
		}
	}
}

func (e *Engine) refreshDue(ctx context.Context) {
	now := time.Now()
	for _, sub := range e.store.Get().Subscriptions {
		if !sub.Enabled || sub.URL == "" {
			continue
		}
		interval := sub.Interval.D()
		if interval <= 0 {
			interval = 12 * time.Hour
		}
		if !sub.LastUpdate.IsZero() && now.Sub(sub.LastUpdate) < interval {
			continue
		}
		if err := e.RefreshSubscription(ctx, sub.ID); err != nil {
			e.logger.Warnf("订阅「%s」更新失败：%v", sub.Name, err)
		}
	}
}

// RefreshSubscription fetches one subscription, stores its nodes and records
// the outcome in the config (which triggers a core reload when it changed).
func (e *Engine) RefreshSubscription(ctx context.Context, id string) error {
	sub := e.store.Get().FindSub(id)
	if sub == nil {
		return fmt.Errorf("订阅不存在")
	}
	if sub.URL == "" {
		return fmt.Errorf("订阅「%s」没有填写链接", sub.Name)
	}

	payload, err := subscription.Fetch(ctx, sub.URL, sub.UserAgent)
	if err == nil {
		var result subscription.Result
		result, err = subscription.Parse(payload.Body, sub.Kind)
		if err == nil {
			if serr := e.cache.Set(sub.ID, result.Nodes, result.Format); serr != nil {
				e.logger.Warnf("写入订阅缓存失败：%v", serr)
			}
			usable := subscription.CountUsable(result.Nodes)
			e.logger.Infof("订阅「%s」更新成功：%d 个节点（可用 %d）", sub.Name, len(result.Nodes), usable)
			return e.store.Update(func(c *config.Config) error {
				if target := c.FindSub(sub.ID); target != nil {
					target.LastUpdate = time.Now()
					target.LastStatus = "ok"
					target.LastError = ""
					target.NodeCount = len(result.Nodes)
					// Store what this fetch reported, so a provider that stops
					// sending the header stops showing a stale quota.
					target.UserInfo = payload.UserInfo
				}
				return nil
			})
		}
	}

	e.logger.Warnf("订阅「%s」更新失败：%v", sub.Name, err)
	return e.store.Update(func(c *config.Config) error {
		if target := c.FindSub(sub.ID); target != nil {
			target.LastStatus = "error"
			target.LastError = err.Error()
		}
		return nil
	})
}

// Traffic returns a copy of the accumulated per-user totals.
func (e *Engine) Traffic() map[string]config.Traffic {
	e.trafficMu.Lock()
	defer e.trafficMu.Unlock()
	out := make(map[string]config.Traffic, len(e.totals))
	for id, t := range e.totals {
		out[id] = t
	}
	return out
}

// OnlineIPs returns the client IPs currently connected per account.
func (e *Engine) OnlineIPs() map[string][]string {
	e.attrMu.RLock()
	defer e.attrMu.RUnlock()
	out := make(map[string][]string, len(e.online))
	for name, ips := range e.online {
		list := make([]string, len(ips))
		copy(list, ips)
		out[name] = list
	}
	return out
}

// ResetTraffic zeroes the accumulated counters of the given users (all users
// when ids is empty).
func (e *Engine) ResetTraffic(ids []string) {
	e.trafficMu.Lock()
	defer e.trafficMu.Unlock()
	if len(ids) == 0 {
		e.totals = map[string]config.Traffic{}
		e.dirty = true
		return
	}
	for _, id := range ids {
		delete(e.totals, id)
	}
	e.dirty = true
}
