package api

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ldm0206/vless-to-http/internal/auth"
	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/help"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/subscription"
	"github.com/ldm0206/vless-to-http/internal/version"
)

// --- public endpoints ----------------------------------------------------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := s.eng.Store().Get()
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"version":    version.Version,
		"xray":       version.Xray(),
		"uptime_sec": int64(time.Since(s.started).Seconds()),
		"users":      len(cfg.Users),
		"subs":       len(cfg.Subscriptions),
	})
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	cfg := s.eng.Store().Get()
	resp := map[string]any{
		"authenticated": false,
		"turnstile": map[string]any{
			"enabled":   cfg.Panel.Admin.Turnstile.Enabled && cfg.Panel.Admin.Turnstile.SiteKey != "",
			"site_key":  cfg.Panel.Admin.Turnstile.SiteKey,
			"fail_open": cfg.Panel.Admin.Turnstile.FailOpen,
		},
		"version": versionInfo(),
	}
	if sess, ok := s.parseSession(r); ok {
		resp["authenticated"] = true
		resp["user"] = sess.User
		resp["csrf"] = sess.CSRF
	}
	s.writeJSON(w, http.StatusOK, resp)
}

type loginRequest struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	Turnstile string `json:"turnstile_token"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if blocked, retry := s.login.blocked(ip); blocked {
		s.logger.Warnf("面板登录被限流：%s", ip)
		s.writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":       "尝试次数过多，请稍后再试",
			"retry_after": int(retry.Seconds()) + 1,
		})
		return
	}

	var req loginRequest
	if !s.readJSON(w, r, &req) {
		return
	}
	cfg := s.eng.Store().Get()

	if cfg.Panel.Admin.Turnstile.Enabled && cfg.Panel.Admin.Turnstile.SecretKey != "" {
		ok, err := s.verifyTurnstile(r.Context(), cfg.Panel.Admin.Turnstile, req.Turnstile, ip)
		if err != nil && !cfg.Panel.Admin.Turnstile.FailOpen {
			s.logger.Warnf("Turnstile 校验异常：%v", err)
			s.writeError(w, http.StatusBadRequest, "人机校验失败，请重试")
			return
		}
		if !ok && err == nil {
			s.login.fail(ip)
			s.writeError(w, http.StatusBadRequest, "人机校验未通过，请重试")
			return
		}
	}

	expectedUser := cfg.Panel.Admin.Username
	userOK := subtleEqual(req.Username, expectedUser)
	passOK := auth.VerifyPassword(cfg.Panel.Admin.PasswordHash, req.Password)
	if !userOK || !passOK {
		s.login.fail(ip)
		s.logger.Warnf("面板登录失败：%s（来自 %s）", req.Username, ip)
		s.writeError(w, http.StatusUnauthorized, "用户名或密码不正确")
		return
	}

	s.login.reset(ip)
	sess := s.issueSession(w, r, cfg.Panel.Admin.Username)
	s.logger.Infof("面板登录成功：%s（来自 %s）", cfg.Panel.Admin.Username, ip)
	s.writeJSON(w, http.StatusOK, map[string]any{
		"ok":   true,
		"user": sess.User,
		"csrf": sess.CSRF,
	})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	// Logging out is idempotent, so an expired session still succeeds - but a
	// live one must present its CSRF token, otherwise any page could log the
	// operator out.
	if sess, ok := s.parseSession(r); ok {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(sess.CSRF)) != 1 {
			s.writeError(w, http.StatusForbidden, "CSRF 校验失败，请刷新页面后重试")
			return
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- read endpoints ------------------------------------------------------

type statusResponse struct {
	Status  engine.Status  `json:"status"`
	Version map[string]any `json:"version"`
	Proxy   map[string]any `json:"proxy"`
	Panel   map[string]any `json:"panel"`
	Health  map[string]any `json:"health"`
	Logs    map[string]any `json:"logs"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	cfg := s.eng.Store().Get()
	s.writeJSON(w, http.StatusOK, statusResponse{
		Status:  s.eng.Status(),
		Version: versionInfo(),
		Proxy: map[string]any{
			"http":        cfg.Proxy.HTTP.Listen,
			"http_on":     cfg.Proxy.HTTP.Enabled,
			"socks":       cfg.Proxy.SOCKS.Listen,
			"socks_on":    cfg.Proxy.SOCKS.Enabled,
			"socks_udp":   cfg.Proxy.SOCKS.UDP,
			"fallback":    cfg.Proxy.Fallback,
			"sniffing":    cfg.Proxy.Sniffing,
			"dns_servers": cfg.Proxy.DNSServers,
		},
		Panel: map[string]any{
			"listen":     cfg.Panel.Listen,
			"public_url": cfg.Panel.PublicURL,
			"theme":      cfg.Panel.Theme,
		},
		Health: map[string]any{
			"enabled":    cfg.Health.Enabled,
			"probe_url":  cfg.Health.ProbeURL,
			"interval":   cfg.Health.Interval.String(),
			"timeout":    cfg.Health.Timeout.String(),
			"thresholds": fmt.Sprintf("连续 %d 次失败判死 / %d 次成功判活", cfg.Health.Failures, cfg.Health.Successes),
		},
		Logs: map[string]any{
			"level":       cfg.Logs.Level,
			"access_log":  cfg.Logs.AccessLog,
			"max_size_mb": cfg.Logs.MaxSizeMB,
			"max_backups": cfg.Logs.MaxBackups,
			"dir":         cfg.Logs.Dir,
		},
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, versionInfo())
}

func (s *Server) handleHelp(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]any{
		"commands":     help.Commands,
		"tui_keys":     help.TUIKeys,
		"global_flags": help.GlobalFlags,
		"cli_text":     help.Text(),
		"tui_text":     help.TUIText(),
	})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := logs.Filter{
		Query: q.Get("q"),
		User:  q.Get("user"),
	}
	if since := q.Get("since"); since != "" {
		if v, err := strconv.ParseUint(since, 10, 64); err == nil {
			filter.Since = v
		}
	}
	if limit := q.Get("limit"); limit != "" {
		if v, err := strconv.Atoi(limit); err == nil {
			filter.Limit = v
		}
	}
	if filter.Limit <= 0 || filter.Limit > 1000 {
		filter.Limit = 200
	}
	if level := q.Get("level"); level != "" {
		filter.Level = logs.ParseLevel(level)
	}

	entries := s.logger.Tail(filter)
	last := uint64(0)
	if len(entries) > 0 {
		last = entries[len(entries)-1].Seq
	}
	s.writeJSON(w, http.StatusOK, map[string]any{
		"entries":  entries,
		"last_seq": last,
		"dropped":  s.logger.Dropped(),
	})
}

// applyNow pushes the change into the core before the response is written, so
// the client sees the effect of its own mutation instead of the state from
// before it. The engine's own debounced apply that follows is a no-op because
// the generated config is already current.
func (s *Server) applyNow() {
	if err := s.eng.Apply("面板修改"); err != nil {
		s.logger.Warnf("应用配置失败：%v", err)
	}
}

// --- users ---------------------------------------------------------------

func (s *Server) handleUserList(w http.ResponseWriter, r *http.Request) {
	status := s.eng.Status()
	cfg := s.eng.Store().Get()
	for i := range status.Users {
		if u := cfg.FindUser(status.Users[i].ID); u != nil {
			status.Users[i].Password = u.Password
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"users": status.Users})
}

func (s *Server) handleUserGet(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user, err := s.userStatus(id)
	if err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"user": user})
}

func (s *Server) userStatus(id string) (*engine.UserStatus, error) {
	status := s.eng.Status()
	cfg := s.eng.Store().Get()
	for i := range status.Users {
		if status.Users[i].ID == id || status.Users[i].Name == id {
			out := status.Users[i]
			if u := cfg.FindUser(out.ID); u != nil {
				out.Password = u.Password
			}
			return &out, nil
		}
	}
	return nil, fmt.Errorf("账号 %q 不存在", id)
}

type targetInput = config.Target

type userInput struct {
	Name     *string        `json:"name"`
	Password *string        `json:"password"`
	Enabled  *bool          `json:"enabled"`
	Mode     *string        `json:"mode"`
	Fallback *string        `json:"fallback"`
	Note     *string        `json:"note"`
	Targets  *[]targetInput `json:"targets"`
}

func (s *Server) handleUserCreate(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if !s.readJSON(w, r, &in) {
		return
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		s.writeError(w, http.StatusBadRequest, "必须填写用户名")
		return
	}
	name := strings.TrimSpace(*in.Name)
	password := ""
	if in.Password != nil {
		password = *in.Password
	}
	if password == "" {
		password = config.NewPassword(16)
	}

	user := config.User{
		ID:        config.NewID("usr"),
		Name:      name,
		Password:  password,
		Enabled:   true,
		Mode:      config.ModePriority,
		Fallback:  config.FallbackInherit,
		CreatedAt: time.Now(),
	}
	if in.Enabled != nil {
		user.Enabled = *in.Enabled
	}
	if in.Mode != nil {
		user.Mode = *in.Mode
	}
	if in.Fallback != nil {
		user.Fallback = *in.Fallback
	}
	if in.Note != nil {
		user.Note = *in.Note
	}
	if in.Targets != nil {
		user.Targets = *in.Targets
	}

	if err := s.eng.Store().Update(func(c *config.Config) error {
		if c.FindUser(name) != nil {
			return fmt.Errorf("账号名 %q 已存在", name)
		}
		if len(c.Users) >= 500 {
			return fmt.Errorf("账号数量已达上限")
		}
		c.Users = append(c.Users, user)
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	created, err := s.userStatus(user.ID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Infof("已创建账号 %s", name)
	s.applyNow()
	s.writeJSON(w, http.StatusOK, map[string]any{"user": created})
}

func (s *Server) handleUserUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in userInput
	if !s.readJSON(w, r, &in) {
		return
	}

	var updatedName string
	if err := s.eng.Store().Update(func(c *config.Config) error {
		user := c.FindUser(id)
		if user == nil {
			return fmt.Errorf("账号 %q 不存在", id)
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" {
				return fmt.Errorf("用户名不能为空")
			}
			for i := range c.Users {
				if c.Users[i].ID != user.ID && c.Users[i].Name == name {
					return fmt.Errorf("账号名 %q 已存在", name)
				}
			}
			user.Name = name
		}
		if in.Password != nil && *in.Password != "" {
			user.Password = *in.Password
		}
		if in.Enabled != nil {
			user.Enabled = *in.Enabled
		}
		if in.Mode != nil {
			user.Mode = *in.Mode
		}
		if in.Fallback != nil {
			user.Fallback = *in.Fallback
		}
		if in.Note != nil {
			user.Note = *in.Note
		}
		if in.Targets != nil {
			user.Targets = *in.Targets
		}
		updatedName = user.Name
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	updated, err := s.userStatus(id)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Infof("已更新账号 %s", updatedName)
	s.applyNow()
	s.writeJSON(w, http.StatusOK, map[string]any{"user": updated})
}

func (s *Server) handleUserDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var removed string
	if err := s.eng.Store().Update(func(c *config.Config) error {
		for i := range c.Users {
			if c.Users[i].ID == id || c.Users[i].Name == id {
				removed = c.Users[i].Name
				c.Users = append(c.Users[:i], c.Users[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("账号 %q 不存在", id)
	}); err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	s.logger.Infof("已删除账号 %s", removed)
	s.applyNow()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleUserTrafficReset(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	user := s.eng.Store().Get().FindUser(id)
	if user == nil {
		s.writeError(w, http.StatusNotFound, "账号不存在")
		return
	}
	s.eng.ResetTraffic([]string{user.ID})
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// --- subscriptions -------------------------------------------------------

func (s *Server) handleSubList(w http.ResponseWriter, r *http.Request) {
	status := s.eng.Status()
	s.writeJSON(w, http.StatusOK, map[string]any{"subs": status.Subs})
}

func (s *Server) handleSubNodes(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	cfg := s.eng.Store().Get()
	sub := cfg.FindSub(id)
	if sub == nil {
		s.writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	nodes := []engine.NodeStatus{}
	for _, n := range s.eng.Nodes() {
		if n.SubID == sub.ID {
			nodes = append(nodes, n)
		}
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"nodes": nodes})
}

type subInput struct {
	Name      *string `json:"name"`
	URL       *string `json:"url"`
	Kind      *string `json:"kind"`
	Interval  *string `json:"interval"`
	Enabled   *bool   `json:"enabled"`
	UserAgent *string `json:"user_agent"`
	Refresh   *bool   `json:"refresh"`
}

func (s *Server) handleSubCreate(w http.ResponseWriter, r *http.Request) {
	var in subInput
	if !s.readJSON(w, r, &in) {
		return
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		s.writeError(w, http.StatusBadRequest, "必须填写订阅名称")
		return
	}
	sub := config.Subscription{
		ID:       config.NewID("sub"),
		Name:     strings.TrimSpace(*in.Name),
		Kind:     "auto",
		Interval: config.Duration(12 * time.Hour),
		Enabled:  true,
	}
	if in.URL != nil {
		sub.URL = strings.TrimSpace(*in.URL)
	}
	if sub.URL == "" {
		s.writeError(w, http.StatusBadRequest, "必须填写订阅链接（也可以粘贴内容导入）")
		return
	}
	if in.Kind != nil {
		sub.Kind = *in.Kind
	}
	if in.UserAgent != nil {
		sub.UserAgent = *in.UserAgent
	}
	if in.Enabled != nil {
		sub.Enabled = *in.Enabled
	}
	if in.Interval != nil {
		d, err := time.ParseDuration(*in.Interval)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "更新间隔格式不正确，例如 12h、30m")
			return
		}
		sub.Interval = config.Duration(d)
	}

	if err := s.eng.Store().Update(func(c *config.Config) error {
		if c.FindSub(sub.Name) != nil {
			return fmt.Errorf("订阅名称 %q 已存在", sub.Name)
		}
		c.Subscriptions = append(c.Subscriptions, sub)
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	refresh := in.Refresh == nil || *in.Refresh
	if refresh {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			if err := s.eng.RefreshSubscription(ctx, sub.ID); err != nil {
				s.logger.Warnf("订阅「%s」首次拉取失败：%v", sub.Name, err)
			}
		}()
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"sub": s.subStatus(sub.ID)})
}

func (s *Server) handleSubUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var in subInput
	if !s.readJSON(w, r, &in) {
		return
	}

	if err := s.eng.Store().Update(func(c *config.Config) error {
		sub := c.FindSub(id)
		if sub == nil {
			return fmt.Errorf("订阅不存在")
		}
		if in.Name != nil {
			name := strings.TrimSpace(*in.Name)
			if name == "" {
				return fmt.Errorf("订阅名称不能为空")
			}
			for i := range c.Subscriptions {
				if c.Subscriptions[i].ID != sub.ID && c.Subscriptions[i].Name == name {
					return fmt.Errorf("订阅名称 %q 已存在", name)
				}
			}
			sub.Name = name
		}
		if in.URL != nil {
			sub.URL = strings.TrimSpace(*in.URL)
		}
		if in.Kind != nil {
			sub.Kind = *in.Kind
		}
		if in.UserAgent != nil {
			sub.UserAgent = *in.UserAgent
		}
		if in.Enabled != nil {
			sub.Enabled = *in.Enabled
		}
		if in.Interval != nil {
			d, err := time.ParseDuration(*in.Interval)
			if err != nil {
				return fmt.Errorf("更新间隔格式不正确，例如 12h、30m")
			}
			sub.Interval = config.Duration(d)
		}
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"sub": s.subStatus(id)})
}

func (s *Server) handleSubDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var removed string
	if err := s.eng.Store().Update(func(c *config.Config) error {
		for i := range c.Subscriptions {
			if c.Subscriptions[i].ID == id || c.Subscriptions[i].Name == id {
				removed = c.Subscriptions[i].Name
				subID := c.Subscriptions[i].ID
				c.Subscriptions = append(c.Subscriptions[:i], c.Subscriptions[i+1:]...)
				for j := range c.Users {
					c.Users[j].Targets = filterTargets(c.Users[j].Targets, subID)
				}
				return nil
			}
		}
		return fmt.Errorf("订阅不存在")
	}); err != nil {
		s.writeError(w, http.StatusNotFound, err.Error())
		return
	}
	if err := s.eng.Cache().Delete(id); err != nil {
		s.logger.Warnf("清理订阅缓存失败：%v", err)
	}
	s.logger.Infof("已删除订阅 %s", removed)
	s.applyNow()
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func filterTargets(targets []config.Target, subID string) []config.Target {
	out := targets[:0]
	for _, t := range targets {
		if t.Sub != subID {
			out = append(out, t)
		}
	}
	return out
}

func (s *Server) handleSubRefresh(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	sub := s.eng.Store().Get().FindSub(id)
	if sub == nil {
		s.writeError(w, http.StatusNotFound, "订阅不存在")
		return
	}
	if sub.URL == "" {
		s.writeError(w, http.StatusBadRequest, "该订阅是本地导入的，没有可更新的链接")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	err := s.eng.RefreshSubscription(ctx, sub.ID)
	resp := map[string]any{"sub": s.subStatus(sub.ID)}
	if err != nil {
		resp["error"] = err.Error()
	}
	s.writeJSON(w, http.StatusOK, resp)
}

type importInput struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

func (s *Server) handleSubImport(w http.ResponseWriter, r *http.Request) {
	var in importInput
	if !s.readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Name) == "" {
		s.writeError(w, http.StatusBadRequest, "必须填写订阅名称")
		return
	}
	if strings.TrimSpace(in.Content) == "" {
		s.writeError(w, http.StatusBadRequest, "内容为空")
		return
	}
	if in.Kind == "" {
		in.Kind = "auto"
	}

	result, err := subscription.Parse([]byte(in.Content), in.Kind)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sub := config.Subscription{
		ID:         config.NewID("sub"),
		Name:       strings.TrimSpace(in.Name),
		Kind:       in.Kind,
		Enabled:    true,
		Interval:   config.Duration(0),
		LastStatus: "ok",
		NodeCount:  len(result.Nodes),
		LastUpdate: time.Now(),
	}
	if err := s.eng.Store().Update(func(c *config.Config) error {
		if c.FindSub(sub.Name) != nil {
			return fmt.Errorf("订阅名称 %q 已存在", sub.Name)
		}
		c.Subscriptions = append(c.Subscriptions, sub)
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng.Cache().Set(sub.ID, result.Nodes, result.Format); err != nil {
		s.writeError(w, http.StatusInternalServerError, "写入节点缓存失败："+err.Error())
		return
	}
	s.logger.Infof("已导入订阅「%s」：%d 个节点", sub.Name, len(result.Nodes))
	s.applyNow()
	s.writeJSON(w, http.StatusOK, map[string]any{"sub": s.subStatus(sub.ID)})
}

func (s *Server) subStatus(id string) *engine.SubStatus {
	status := s.eng.Status()
	for i := range status.Subs {
		if status.Subs[i].ID == id {
			return &status.Subs[i]
		}
	}
	return nil
}

// --- nodes ---------------------------------------------------------------

func (s *Server) handleNodeList(w http.ResponseWriter, r *http.Request) {
	subFilter := r.URL.Query().Get("sub")
	onlyUsable := r.URL.Query().Get("only_usable") == "1"

	cfg := s.eng.Store().Get()
	subID := subFilter
	if subFilter != "" {
		if sub := cfg.FindSub(subFilter); sub != nil {
			subID = sub.ID
		}
	}

	nodes := s.eng.Nodes()
	out := make([]engine.NodeStatus, 0, len(nodes))
	for _, n := range nodes {
		if subID != "" && n.SubID != subID {
			continue
		}
		if onlyUsable && n.Unsupported != "" {
			continue
		}
		out = append(out, n)
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

type nodeTestRequest struct {
	IDs []string `json:"ids"`
	Sub string   `json:"sub"`
}

func (s *Server) handleNodeTest(w http.ResponseWriter, r *http.Request) {
	var in nodeTestRequest
	if !s.readJSON(w, r, &in) {
		return
	}

	ids := in.IDs
	if in.Sub != "" && len(ids) == 0 {
		cfg := s.eng.Store().Get()
		subID := in.Sub
		if sub := cfg.FindSub(in.Sub); sub != nil {
			subID = sub.ID
		}
		for _, n := range s.eng.Nodes() {
			if n.SubID == subID && n.Unsupported == "" {
				ids = append(ids, n.ID)
			}
		}
	}
	if len(ids) == 0 {
		s.writeError(w, http.StatusBadRequest, "没有指定要测试的节点")
		return
	}
	if len(ids) > 200 {
		ids = ids[:200]
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	results := s.eng.TestNodes(ctx, ids)
	s.writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// --- settings ------------------------------------------------------------

func (s *Server) handleSettingsGet(w http.ResponseWriter, r *http.Request) {
	cfg := s.eng.Store().Get()
	s.writeJSON(w, http.StatusOK, settingsView(cfg))
}

func settingsView(cfg *config.Config) map[string]any {
	return map[string]any{
		"panel": map[string]any{
			"listen":          cfg.Panel.Listen,
			"public_url":      cfg.Panel.PublicURL,
			"session_hours":   cfg.Panel.SessionHours,
			"token_ips":       cfg.Panel.TokenIPs,
			"trusted_proxies": cfg.Panel.TrustedProxies,
			"api_token":       cfg.Panel.APIToken,
			"theme":           cfg.Panel.Theme,
			"admin": map[string]any{
				"username":     cfg.Panel.Admin.Username,
				"password_set": cfg.Panel.Admin.PasswordHash != "",
			},
			"turnstile": map[string]any{
				"enabled":    cfg.Panel.Admin.Turnstile.Enabled,
				"site_key":   cfg.Panel.Admin.Turnstile.SiteKey,
				"secret_set": cfg.Panel.Admin.Turnstile.SecretKey != "",
				"fail_open":  cfg.Panel.Admin.Turnstile.FailOpen,
			},
		},
		"proxy": cfg.Proxy,
		"logs":  cfg.Logs,
		"health": map[string]any{
			"enabled":         cfg.Health.Enabled,
			"probe_url":       cfg.Health.ProbeURL,
			"interval":        cfg.Health.Interval.String(),
			"timeout":         cfg.Health.Timeout.String(),
			"failures":        cfg.Health.Failures,
			"successes":       cfg.Health.Successes,
			"switch_cooldown": cfg.Health.SwitchCooldown.String(),
			"max_probes":      cfg.Health.MaxProbes,
		},
	}
}

type settingsInput struct {
	Panel  *panelInput  `json:"panel"`
	Proxy  *proxyInput  `json:"proxy"`
	Logs   *logsInput   `json:"logs"`
	Health *healthInput `json:"health"`
}

type panelInput struct {
	Listen         *string   `json:"listen"`
	PublicURL      *string   `json:"public_url"`
	SessionHours   *int      `json:"session_hours"`
	TokenIPs       *[]string `json:"token_ips"`
	TrustedProxies *[]string `json:"trusted_proxies"`
	Theme          *string   `json:"theme"`
	Turnstile      *struct {
		Enabled   *bool   `json:"enabled"`
		SiteKey   *string `json:"site_key"`
		SecretKey *string `json:"secret_key"`
		FailOpen  *bool   `json:"fail_open"`
	} `json:"turnstile"`
}

type proxyInput struct {
	HTTP       *config.Listener      `json:"http"`
	SOCKS      *config.SocksListener `json:"socks"`
	Sniffing   *bool                 `json:"sniffing"`
	Fallback   *string               `json:"fallback"`
	DNSServers *[]string             `json:"dns_servers"`
	TimeoutSec *int                  `json:"timeout_sec"`
}

type logsInput struct {
	Level      *string `json:"level"`
	AccessLog  *bool   `json:"access_log"`
	Dir        *string `json:"dir"`
	MaxSizeMB  *int    `json:"max_size_mb"`
	MaxBackups *int    `json:"max_backups"`
	RingSize   *int    `json:"ring_size"`
	Console    *bool   `json:"console"`
}

type healthInput struct {
	Enabled        *bool   `json:"enabled"`
	ProbeURL       *string `json:"probe_url"`
	Interval       *string `json:"interval"`
	Timeout        *string `json:"timeout"`
	Failures       *int    `json:"failures"`
	Successes      *int    `json:"successes"`
	SwitchCooldown *string `json:"switch_cooldown"`
	MaxProbes      *int    `json:"max_probes"`
}

func (s *Server) handleSettingsPatch(w http.ResponseWriter, r *http.Request) {
	var in settingsInput
	if !s.readJSON(w, r, &in) {
		return
	}

	var logsChanged bool
	if err := s.eng.Store().Update(func(c *config.Config) error {
		if in.Panel != nil {
			if in.Panel.Listen != nil {
				c.Panel.Listen = strings.TrimSpace(*in.Panel.Listen)
			}
			if in.Panel.PublicURL != nil {
				c.Panel.PublicURL = strings.TrimSpace(*in.Panel.PublicURL)
			}
			if in.Panel.SessionHours != nil && *in.Panel.SessionHours > 0 {
				c.Panel.SessionHours = *in.Panel.SessionHours
			}
			if in.Panel.TokenIPs != nil {
				c.Panel.TokenIPs = *in.Panel.TokenIPs
			}
			if in.Panel.TrustedProxies != nil {
				c.Panel.TrustedProxies = *in.Panel.TrustedProxies
			}
			if in.Panel.Theme != nil {
				c.Panel.Theme = *in.Panel.Theme
			}
			if in.Panel.Turnstile != nil {
				t := &c.Panel.Admin.Turnstile
				if in.Panel.Turnstile.Enabled != nil {
					t.Enabled = *in.Panel.Turnstile.Enabled
				}
				if in.Panel.Turnstile.SiteKey != nil {
					t.SiteKey = strings.TrimSpace(*in.Panel.Turnstile.SiteKey)
				}
				if in.Panel.Turnstile.SecretKey != nil && *in.Panel.Turnstile.SecretKey != "" {
					t.SecretKey = strings.TrimSpace(*in.Panel.Turnstile.SecretKey)
				}
				if in.Panel.Turnstile.FailOpen != nil {
					t.FailOpen = *in.Panel.Turnstile.FailOpen
				}
			}
		}

		if in.Proxy != nil {
			if in.Proxy.HTTP != nil {
				c.Proxy.HTTP = *in.Proxy.HTTP
			}
			if in.Proxy.SOCKS != nil {
				c.Proxy.SOCKS = *in.Proxy.SOCKS
			}
			if in.Proxy.Sniffing != nil {
				c.Proxy.Sniffing = *in.Proxy.Sniffing
			}
			if in.Proxy.Fallback != nil {
				c.Proxy.Fallback = *in.Proxy.Fallback
			}
			if in.Proxy.DNSServers != nil {
				c.Proxy.DNSServers = *in.Proxy.DNSServers
			}
			if in.Proxy.TimeoutSec != nil && *in.Proxy.TimeoutSec > 0 {
				c.Proxy.Timeout = config.Duration(time.Duration(*in.Proxy.TimeoutSec) * time.Second)
			}
		}

		if in.Logs != nil {
			logsChanged = true
			if in.Logs.Level != nil {
				c.Logs.Level = *in.Logs.Level
			}
			if in.Logs.AccessLog != nil {
				c.Logs.AccessLog = *in.Logs.AccessLog
			}
			if in.Logs.Dir != nil {
				c.Logs.Dir = strings.TrimSpace(*in.Logs.Dir)
			}
			if in.Logs.MaxSizeMB != nil && *in.Logs.MaxSizeMB > 0 {
				c.Logs.MaxSizeMB = *in.Logs.MaxSizeMB
			}
			if in.Logs.MaxBackups != nil && *in.Logs.MaxBackups > 0 {
				c.Logs.MaxBackups = *in.Logs.MaxBackups
			}
			if in.Logs.RingSize != nil && *in.Logs.RingSize > 0 {
				c.Logs.RingSize = *in.Logs.RingSize
			}
			if in.Logs.Console != nil {
				c.Logs.Console = *in.Logs.Console
			}
		}

		if in.Health != nil {
			if in.Health.Enabled != nil {
				c.Health.Enabled = *in.Health.Enabled
			}
			if in.Health.ProbeURL != nil {
				c.Health.ProbeURL = strings.TrimSpace(*in.Health.ProbeURL)
			}
			if in.Health.Failures != nil && *in.Health.Failures > 0 {
				c.Health.Failures = *in.Health.Failures
			}
			if in.Health.Successes != nil && *in.Health.Successes > 0 {
				c.Health.Successes = *in.Health.Successes
			}
			if in.Health.MaxProbes != nil && *in.Health.MaxProbes > 0 {
				c.Health.MaxProbes = *in.Health.MaxProbes
			}
			for _, pair := range []struct {
				raw  *string
				dest *config.Duration
			}{
				{in.Health.Interval, &c.Health.Interval},
				{in.Health.Timeout, &c.Health.Timeout},
				{in.Health.SwitchCooldown, &c.Health.SwitchCooldown},
			} {
				if pair.raw == nil || *pair.raw == "" {
					continue
				}
				d, err := time.ParseDuration(*pair.raw)
				if err != nil {
					return fmt.Errorf("时间格式不正确：%q", *pair.raw)
				}
				*pair.dest = config.Duration(d)
			}
		}
		return nil
	}); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	s.applyNow()

	if logsChanged {
		cfg := s.eng.Store().Get()
		if err := s.logger.SetOptions(logs.Options{
			Level:      logs.ParseLevel(cfg.Logs.Level),
			AccessLog:  cfg.Logs.AccessLog,
			Dir:        cfg.LogDir(s.eng.Store().DataDir()),
			MaxSizeMB:  cfg.Logs.MaxSizeMB,
			MaxBackups: cfg.Logs.MaxBackups,
			RingSize:   cfg.Logs.RingSize,
			Console:    cfg.Logs.Console,
		}); err != nil {
			s.logger.Warnf("应用日志设置失败：%v", err)
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]any{"settings": settingsView(s.eng.Store().Get())})
}

type passwordInput struct {
	Current  string `json:"current"`
	Password string `json:"password"`
}

func (s *Server) handleAdminPassword(w http.ResponseWriter, r *http.Request) {
	var in passwordInput
	if !s.readJSON(w, r, &in) {
		return
	}
	cfg := s.eng.Store().Get()
	if !auth.VerifyPassword(cfg.Panel.Admin.PasswordHash, in.Current) {
		s.writeError(w, http.StatusForbidden, "当前密码不正确")
		return
	}
	if len(in.Password) < 8 {
		s.writeError(w, http.StatusBadRequest, "新密码至少 8 位")
		return
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.eng.Store().Update(func(c *config.Config) error {
		c.Panel.Admin.PasswordHash = hash
		return nil
	}); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Infof("管理员密码已更新")
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleTokenRotate(w http.ResponseWriter, r *http.Request) {
	token := config.NewToken()
	if err := s.eng.Store().Update(func(c *config.Config) error {
		c.Panel.APIToken = token
		return nil
	}); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logger.Infof("API Token 已轮换")
	s.writeJSON(w, http.StatusOK, map[string]any{"api_token": token})
}

// --- core ----------------------------------------------------------------

func (s *Server) handleCoreRestart(w http.ResponseWriter, r *http.Request) {
	if err := s.eng.ForceRestart(); err != nil {
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCoreConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.eng.PlanJSON()))
}

// sortedUsers keeps API output stable for tests and diffs.
func sortedUsers(users []engine.UserStatus) []engine.UserStatus {
	sort.SliceStable(users, func(i, j int) bool { return users[i].Name < users[j].Name })
	return users
}
