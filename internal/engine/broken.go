package engine

import (
	"regexp"
	"strings"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// tagPattern extracts the outbound tag Xray names when it refuses to build a
// config: "… failed to build outbound config with tag node:abc123 > …".
var tagPattern = regexp.MustCompile(`with tag ([^\s>]+)`)

// maxRebuildAttempts bounds how many nodes we are willing to drop in one
// apply, so a systematically broken subscription cannot spin here.
const maxRebuildAttempts = 8

// startWithRetry boots the core, and when it refuses a specific outbound,
// drops that node and tries again. A single malformed server in a
// subscription must not take the proxy down for every account.
func (e *Engine) startWithRetry(cfg *config.Config, res *resolution, raw []byte) (*xraycore.Instance, *resolution, []byte, error) {
	inst, err := xraycore.Start(raw, e.onXrayLog)

	for attempt := 0; err != nil && attempt < maxRebuildAttempts; attempt++ {
		tag := brokenTag(err)
		nodeID, ok := nodeIDFromTag(tag)
		if !ok {
			break
		}
		e.markBroken(nodeID, err.Error())

		// Rebuild without the rejected node and note the new resolution: the
		// caller stores it as the state the running core reflects.
		res = resolve(cfg, e.nodesWithPins(), e.prober.Healthy, e.brokenNodes(), e.probeAddr, e.probePass)
		raw, buildErr := xraycore.BuildJSON(res.Plan)
		if buildErr != nil {
			return nil, res, raw, buildErr
		}
		e.logger.Warnf("节点 %s 的内核配置无效（%v），已剔除后重试", e.nodeName(nodeID), firstLine(err.Error()))
		inst, err = xraycore.Start(raw, e.onXrayLog)
	}
	return inst, res, raw, err
}

// brokenTag returns the tag Xray complained about, if it named one.
func brokenTag(err error) string {
	if err == nil {
		return ""
	}
	match := tagPattern.FindStringSubmatch(err.Error())
	if len(match) < 2 {
		return ""
	}
	return match[1]
}

// nodeIDFromTag turns "node:abc123" into "abc123".
func nodeIDFromTag(tag string) (string, bool) {
	prefix := "node:"
	if !strings.HasPrefix(tag, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(tag, prefix)
	if id == "" {
		return "", false
	}
	return id, true
}

func (e *Engine) markBroken(nodeID, reason string) {
	e.brokenMu.Lock()
	if e.broken == nil {
		e.broken = map[string]string{}
	}
	e.broken[nodeID] = coreReason(reason)
	e.brokenMu.Unlock()
}

// brokenNodes copies the current set of rejected nodes.
func (e *Engine) brokenNodes() map[string]string {
	e.brokenMu.RLock()
	defer e.brokenMu.RUnlock()
	if len(e.broken) == 0 {
		return nil
	}
	out := make(map[string]string, len(e.broken))
	for id, reason := range e.broken {
		out[id] = reason
	}
	return out
}

// BrokenNodes exposes the rejected nodes for the panels.
func (e *Engine) BrokenNodes() map[string]string { return e.brokenNodes() }

// forgetMissingBroken drops entries for nodes that no longer exist, so a
// subscription update that replaces them starts from a clean slate.
func (e *Engine) forgetMissingBroken() {
	present := map[string]bool{}
	for _, nodes := range e.cache.All() {
		for i := range nodes {
			present[nodes[i].ID] = true
		}
	}

	e.brokenMu.Lock()
	for id := range e.broken {
		if !present[id] {
			delete(e.broken, id)
		}
	}
	e.brokenMu.Unlock()
}

// coreReason distils Xray's layered error into the most specific sentence,
// so a panel cell says `invalid "password": 6ECf…` instead of the whole chain.
func coreReason(s string) string {
	if idx := strings.LastIndex(s, "> "); idx >= 0 {
		s = strings.TrimSpace(s[idx+2:])
	}
	s = strings.TrimPrefix(s, "infra/conf: ")
	return firstLine(s)
}

// firstLine keeps multi-line core errors readable in one table cell.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		s = s[:idx]
	}
	const limit = 200
	if len(s) > limit {
		s = s[:limit] + "…"
	}
	return s
}
