package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleEvents streams status snapshots and log lines to the panel and the TUI
// over Server-Sent Events: one connection, no polling, works through proxies.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "当前连接不支持流式推送")
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.logger.Subscribe()
	defer cancel()

	// Only send log lines the operator asked for; the status feed follows the
	// panel's own refresh cadence.
	q := r.URL.Query()
	logLevel := parseLevelParam(q.Get("level"))
	userFilter := q.Get("user")

	statusTicker := time.NewTicker(2 * time.Second)
	defer statusTicker.Stop()

	s.sendEvent(w, flusher, "status", s.statusPayload())
	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case entry, ok := <-events:
			if !ok {
				return
			}
			if logLevel != "" && entry.Level != "" && levelRank(entry.Level) < levelRank(logLevel) {
				continue
			}
			if userFilter != "" && entry.User != userFilter {
				continue
			}
			s.sendEvent(w, flusher, "log", entry)

		case <-statusTicker.C:
			s.sendEvent(w, flusher, "status", s.statusPayload())

		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Server) statusPayload() any {
	cfg := s.eng.Store().Get()
	status := s.eng.Status()
	// Passwords are deliberately absent from Status(): neither this feed nor
	// /api/status ever carries credentials.
	return map[string]any{
		"status": status,
		"proxy": map[string]any{
			"http":     cfg.Proxy.HTTP.Listen,
			"socks":    cfg.Proxy.SOCKS.Listen,
			"fallback": cfg.Proxy.Fallback,
		},
	}
}

func (s *Server) sendEvent(w http.ResponseWriter, flusher http.Flusher, name string, payload any) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, raw)
	flusher.Flush()
}

func levelRank(level string) int {
	switch level {
	case "debug":
		return 0
	case "info", "":
		return 1
	case "warning", "warn":
		return 2
	case "error":
		return 3
	default:
		return 1
	}
}

func parseLevelParam(level string) string {
	switch level {
	case "debug", "info", "warning", "error":
		return level
	default:
		return ""
	}
}
