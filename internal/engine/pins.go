package engine

import (
	"context"
	"sync"
	"time"

	"github.com/ldm0206/vless-to-http/internal/certpin"
	"github.com/ldm0206/vless-to-http/internal/node"
)

// nodesWithPins returns the cached nodes with the remembered certificate
// decision of each node applied, so compiling a plan never touches the network.
func (e *Engine) nodesWithPins() map[string][]node.Node {
	subs := e.cache.All()
	for id, nodes := range subs {
		for i := range nodes {
			if !nodes[i].NeedsCertPin() {
				continue
			}
			if entry := e.pins.Get(nodes[i].ID); entry.Sha256 != "" {
				nodes[i].PinnedCertSha256 = entry.Sha256
			}
		}
		subs[id] = nodes
	}
	return subs
}

// ensurePins starts a background inspection for every used node whose
// certificate decision is missing or stale. The result only lands in the plan
// on the next apply, so a slow or unreachable host never delays the core.
func (e *Engine) ensurePins(nodes []node.Node) {
	now := time.Now()
	for _, n := range nodes {
		if !n.NeedsCertPin() {
			continue
		}
		if e.pins.Get(n.ID).Fresh(now) {
			continue
		}
		if !e.pinning.claim(n.ID) {
			continue
		}
		go e.inspectPin(n)
	}
}

// inspectPin probes one node's certificate and remembers what it found. A node
// whose certificate cannot be verified gains a pin, which is what lets the core
// dial it at all.
func (e *Engine) inspectPin(n node.Node) {
	defer e.pinning.release(n.ID)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if e.stopped.Load() {
		return
	}

	previous := e.pins.Get(n.ID)
	entry := certpin.Entry{Checked: time.Now()}

	res, err := certpin.Inspect(ctx, certpin.Target{Host: n.Server, Port: n.Port, SNI: n.SNI}, certpin.Options{})
	switch {
	case err != nil:
		entry.LastError = err.Error()
		e.logger.Warnf("节点 %s 的证书探测失败：%v", n.Label(), err)
	case res.Verified:
		entry.Verified = true
		e.logger.Infof("节点 %s 的证书校验通过，无需固定指纹", n.Label())
	default:
		entry.Sha256 = res.Sha256
		e.logger.Infof("节点 %s 的证书无法校验，已固定指纹 %s", n.Label(), shortHash(res.Sha256))
	}

	if err := e.pins.Put(n.ID, entry); err != nil {
		e.logger.Warnf("保存证书指纹失败：%v", err)
	}
	if previous.Sha256 != entry.Sha256 && !e.stopped.Load() {
		// The plan changes either way: the node now carries the pin, or it lost
		// it because the certificate verifies normally again.
		e.scheduleApply("证书指纹更新", false)
	}
}

// forgetMissingPins drops the decisions of nodes that no longer exist, so the
// store cannot grow across subscription updates.
func (e *Engine) forgetMissingPins() {
	present := map[string]bool{}
	for _, nodes := range e.cache.All() {
		for i := range nodes {
			present[nodes[i].ID] = true
		}
	}
	if err := e.pins.Forget(present); err != nil {
		e.logger.Warnf("清理证书指纹缓存失败：%v", err)
	}
}

// shortHash trims a hex digest to something readable in a log line.
func shortHash(digest string) string {
	if len(digest) > 16 {
		return digest[:16] + "…"
	}
	return digest
}

// inflight tracks the node ids whose certificate is currently being inspected,
// so repeated applies do not stack up probes for the same host.
type inflight struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (f *inflight) claim(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ids == nil {
		f.ids = map[string]bool{}
	}
	if f.ids[id] {
		return false
	}
	f.ids[id] = true
	return true
}

func (f *inflight) release(id string) {
	f.mu.Lock()
	delete(f.ids, id)
	f.mu.Unlock()
}
