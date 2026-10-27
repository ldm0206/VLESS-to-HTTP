package xraycore_test

import (
	"encoding/json"
	"testing"

	"github.com/ldm0206/vless-to-http/internal/xraycore"
)

// TestSkipSniffingRulePrecedesCustomRules: an account that opted out of
// sniffing must be routed before the operator's domain rules, otherwise the
// sniffed domain would still steer its traffic.
func TestSkipSniffingRulePrecedesCustomRules(t *testing.T) {
	plan := xraycore.Plan{
		Users: []xraycore.PlanUser{
			{Name: "alice", Password: "pw", OutboundTag: "node:one", SkipSniffing: true},
			{Name: "bob", Password: "pw", OutboundTag: "node:two"},
		},
		CustomRules: []map[string]any{
			{"type": "field", "domain": []any{"example.com"}, "outboundTag": xraycore.DirectTag},
		},
	}

	raw, err := xraycore.BuildJSON(plan)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	var doc struct {
		Routing struct {
			Rules []struct {
				User        []string `json:"user"`
				Domain      []string `json:"domain"`
				OutboundTag string   `json:"outboundTag"`
			} `json:"rules"`
		} `json:"routing"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rules := doc.Routing.Rules
	if len(rules) < 5 {
		t.Fatalf("rules = %+v", rules)
	}
	if len(rules[0].User) != 1 || rules[0].User[0] != "alice" || rules[0].OutboundTag != "node:one" {
		t.Fatalf("first rule should be alice's no-sniff bypass, got %+v", rules[0])
	}
	if len(rules[1].Domain) != 1 || rules[1].Domain[0] != "example.com" {
		t.Fatalf("custom rule should follow the bypass, got %+v", rules[1])
	}
	if len(rules[2].User) != 1 || rules[2].User[0] != "alice" {
		t.Fatalf("normal user rule order changed: %+v", rules)
	}
}
