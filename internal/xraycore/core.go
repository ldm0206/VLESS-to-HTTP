// Package xraycore embeds the Xray-core runtime in-process so the whole proxy
// runs as a single binary with no subprocess and no config file on disk.
package xraycore

import (
	"fmt"

	"github.com/xtls/xray-core/common/log"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/core"
	"github.com/xtls/xray-core/features/stats"

	// Registers every protocol/transport/feature handler that the JSON config
	// loader can refer to.
	_ "github.com/xtls/xray-core/main/distro/all"
)

// LogRecord is one message emitted by the core.
type LogRecord struct {
	// Severity is debug, info, warning or error.
	Severity string
	// Access is true for connection records rather than diagnostics.
	Access bool
	From   string
	To     string
	Status string
	Email  string
	Msg    string
}

// LogHandler receives every message the core produces.
type LogHandler func(LogRecord)

// Traffic is a user's byte counters since the instance started.
type Traffic struct {
	Up   int64
	Down int64
}

// Instance wraps a running Xray core.
type Instance struct {
	inst *core.Instance
}

// Start boots an Xray instance from a JSON config document and routes its log
// stream into handler.
func Start(configJSON []byte, handler LogHandler) (*Instance, error) {
	inst, err := core.StartInstance("json", configJSON)
	if err != nil {
		return nil, fmt.Errorf("启动内核失败：%w", err)
	}
	// The core installs its own handler while booting, so ours has to be
	// registered afterwards or it would be discarded.
	if handler != nil {
		log.RegisterHandler(&logAdapter{fn: handler})
	}
	return &Instance{inst: inst}, nil
}

// Close shuts the instance down and releases its listeners. A closed instance
// can never be restarted; callers must build a new one.
func (i *Instance) Close() error {
	if i == nil || i.inst == nil {
		return nil
	}
	return i.inst.Close()
}

// UserTraffic reads the per-user byte counters. Counters start at zero on
// every start, so the caller accumulates deltas.
func (i *Instance) UserTraffic(names []string) map[string]Traffic {
	out := make(map[string]Traffic, len(names))
	if i == nil || i.inst == nil {
		return out
	}
	manager, ok := i.inst.GetFeature(stats.ManagerType()).(stats.Manager)
	if !ok || manager == nil {
		return out
	}
	for _, name := range names {
		var t Traffic
		if c := manager.GetCounter(uplinkCounter(name)); c != nil {
			t.Up = c.Value()
		}
		if c := manager.GetCounter(downlinkCounter(name)); c != nil {
			t.Down = c.Value()
		}
		out[name] = t
	}
	return out
}

// UserOnline reports the client IPs currently using each account, which is
// also how access-log lines are attributed to a user.
func (i *Instance) UserOnline(names []string) map[string][]string {
	out := make(map[string][]string, len(names))
	if i == nil || i.inst == nil {
		return out
	}
	manager, ok := i.inst.GetFeature(stats.ManagerType()).(stats.Manager)
	if !ok || manager == nil {
		return out
	}
	for _, name := range names {
		om := manager.GetOnlineMap(onlineCounter(name))
		if om == nil {
			continue
		}
		if ips := om.List(); len(ips) > 0 {
			out[name] = ips
		}
	}
	return out
}

// Counter names as the dispatcher writes them.
func uplinkCounter(email string) string   { return "user>>>" + email + ">>>traffic>>>uplink" }
func downlinkCounter(email string) string { return "user>>>" + email + ">>>traffic>>>downlink" }
func onlineCounter(email string) string   { return "user>>>" + email + ">>>online" }

type logAdapter struct {
	fn LogHandler
}

func (a *logAdapter) Handle(msg log.Message) {
	if a.fn == nil {
		return
	}
	switch m := msg.(type) {
	case *log.AccessMessage:
		a.fn(LogRecord{
			Severity: "info",
			Access:   true,
			From:     serial.ToString(m.From),
			To:       serial.ToString(m.To),
			Status:   string(m.Status),
			Email:    m.Email,
			Msg:      m.String(),
		})
	case *log.GeneralMessage:
		a.fn(LogRecord{
			Severity: severityName(m.Severity),
			Msg:      fmt.Sprint(m.Content),
		})
	default:
		a.fn(LogRecord{Severity: "info", Msg: msg.String()})
	}
}

func severityName(s log.Severity) string {
	switch s {
	case log.Severity_Error:
		return "error"
	case log.Severity_Warning:
		return "warning"
	case log.Severity_Debug:
		return "debug"
	default:
		return "info"
	}
}
