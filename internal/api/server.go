// Package api serves the control panel, its JSON API and the SSE feed. The
// CLI and the TUI talk to the same endpoints with a bearer token.
package api

import (
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ldm0206/vless-to-http/internal/engine"
	"github.com/ldm0206/vless-to-http/internal/logs"
	"github.com/ldm0206/vless-to-http/internal/version"
)

// CookieName is the session cookie the panel sets after login.
const CookieName = "v2h_session"

// maxBody caps request bodies; every endpoint takes small JSON documents.
const maxBody = 1 << 20

// Server is the HTTP surface of the panel.
type Server struct {
	eng    *engine.Engine
	logger *logs.Logger
	mux    *http.ServeMux

	trusted []*net.IPNet
	login   *rateLimiter
	started time.Time
}

// New wires up the routes.
func New(eng *engine.Engine, logger *logs.Logger) (*Server, error) {
	s := &Server{
		eng:     eng,
		logger:  logger,
		mux:     http.NewServeMux(),
		trusted: parseCIDRs(eng.Store().Get().Panel.TrustedProxies),
		login:   newRateLimiter(10, 15*time.Minute),
		started: time.Now(),
	}
	if err := s.routes(); err != nil {
		return nil, err
	}
	return s, nil
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	return s.securityHeaders(s.recoverer(s.gzip(s.mux)))
}

// ListenAndServe runs the panel until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      0, // SSE streams stay open
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	errCh := make(chan error, 1)
	go func() {
		s.logger.Infof("控制面板已启动：http://%s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

func (s *Server) routes() error {
	// Public endpoints.
	s.mux.HandleFunc("GET /api/health", s.handleHealth)
	s.mux.HandleFunc("GET /api/session", s.handleSession)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.handleLogout)

	// Everything below needs a session cookie or the API token.
	s.mux.HandleFunc("GET /api/status", s.auth(s.handleStatus))
	s.mux.HandleFunc("GET /api/version", s.auth(s.handleVersion))
	s.mux.HandleFunc("GET /api/help", s.auth(s.handleHelp))
	s.mux.HandleFunc("GET /api/events", s.auth(s.handleEvents))

	s.mux.HandleFunc("GET /api/users", s.auth(s.handleUserList))
	s.mux.HandleFunc("POST /api/users", s.auth(s.handleUserCreate))
	s.mux.HandleFunc("GET /api/users/{id}", s.auth(s.handleUserGet))
	s.mux.HandleFunc("PATCH /api/users/{id}", s.auth(s.handleUserUpdate))
	s.mux.HandleFunc("DELETE /api/users/{id}", s.auth(s.handleUserDelete))
	s.mux.HandleFunc("POST /api/users/{id}/traffic/reset", s.auth(s.handleUserTrafficReset))

	s.mux.HandleFunc("GET /api/subs", s.auth(s.handleSubList))
	s.mux.HandleFunc("POST /api/subs", s.auth(s.handleSubCreate))
	s.mux.HandleFunc("PATCH /api/subs/{id}", s.auth(s.handleSubUpdate))
	s.mux.HandleFunc("DELETE /api/subs/{id}", s.auth(s.handleSubDelete))
	s.mux.HandleFunc("POST /api/subs/{id}/refresh", s.auth(s.handleSubRefresh))
	s.mux.HandleFunc("GET /api/subs/{id}/nodes", s.auth(s.handleSubNodes))
	s.mux.HandleFunc("POST /api/subs/import", s.auth(s.handleSubImport))

	s.mux.HandleFunc("GET /api/nodes", s.auth(s.handleNodeList))
	s.mux.HandleFunc("POST /api/nodes/test", s.auth(s.handleNodeTest))

	s.mux.HandleFunc("GET /api/logs", s.auth(s.handleLogs))

	s.mux.HandleFunc("GET /api/settings", s.auth(s.handleSettingsGet))
	s.mux.HandleFunc("PATCH /api/settings", s.auth(s.handleSettingsPatch))
	s.mux.HandleFunc("POST /api/settings/password", s.auth(s.handleAdminPassword))
	s.mux.HandleFunc("POST /api/settings/token", s.auth(s.handleTokenRotate))

	s.mux.HandleFunc("POST /api/core/restart", s.auth(s.handleCoreRestart))
	s.mux.HandleFunc("GET /api/core/config", s.auth(s.handleCoreConfig))

	static, err := s.staticHandler()
	if err != nil {
		return err
	}
	s.mux.Handle("/", static)
	return nil
}

// --- middleware ----------------------------------------------------------

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"script-src 'self' https://challenges.cloudflare.com; frame-src https://challenges.cloudflare.com; "+
				"connect-src 'self'; base-uri 'none'; form-action 'self'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Errorf("面板处理 %s %s 时崩溃：%v", r.Method, r.URL.Path, rec)
				http.Error(w, `{"error":"内部错误"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) gzip(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") ||
			strings.HasPrefix(r.URL.Path, "/api/events") {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		next.ServeHTTP(&gzipResponseWriter{ResponseWriter: w, Writer: gz}, r)
	})
}

type gzipResponseWriter struct {
	http.ResponseWriter
	io.Writer
}

func (g *gzipResponseWriter) Write(b []byte) (int, error) { return g.Writer.Write(b) }

// --- auth ----------------------------------------------------------------

type sessionData struct {
	User string `json:"u"`
	CSRF string `json:"c"`
	Exp  int64  `json:"e"`
}

func (s *Server) secret() []byte {
	token := s.eng.Store().Get().Panel.APIToken
	if token == "" {
		token = "v2h-fallback-secret"
	}
	sum := sha256.Sum256([]byte("v2h-session:" + token))
	return sum[:]
}

func (s *Server) issueSession(w http.ResponseWriter, r *http.Request, user string) sessionData {
	cfg := s.eng.Store().Get()
	hours := cfg.Panel.SessionHours
	if hours <= 0 {
		hours = 12
	}
	sess := sessionData{
		User: user,
		CSRF: randomToken(),
		Exp:  time.Now().Add(time.Duration(hours) * time.Hour).Unix(),
	}
	payload, _ := json.Marshal(sess)
	encoded := base64.RawURLEncoding.EncodeToString(payload)

	mac := hmac.New(sha256.New, s.secret())
	mac.Write([]byte(encoded))
	value := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	http.SetCookie(w, &http.Cookie{
		Name:     CookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   s.isHTTPS(r),
		MaxAge:   int(time.Duration(hours) * time.Hour / time.Second),
	})
	return sess
}

func (s *Server) parseSession(r *http.Request) (*sessionData, bool) {
	cookie, err := r.Cookie(CookieName)
	if err != nil {
		return nil, false
	}
	parts := strings.SplitN(cookie.Value, ".", 2)
	if len(parts) != 2 {
		return nil, false
	}
	mac := hmac.New(sha256.New, s.secret())
	mac.Write([]byte(parts[0]))
	expected := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(expected), []byte(parts[1])) != 1 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, false
	}
	var sess sessionData
	if err := json.Unmarshal(payload, &sess); err != nil {
		return nil, false
	}
	if time.Now().Unix() > sess.Exp {
		return nil, false
	}
	return &sess, true
}

// auth accepts a panel session or, from allowed addresses, the API token.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if sess, ok := s.parseSession(r); ok {
			if isMutating(r.Method) {
				token := r.Header.Get("X-CSRF-Token")
				if subtle.ConstantTimeCompare([]byte(token), []byte(sess.CSRF)) != 1 {
					s.writeError(w, http.StatusForbidden, "CSRF 校验失败，请刷新页面后重试")
					return
				}
			}
			next(w, r)
			return
		}

		if token := bearerToken(r); token != "" {
			expected := s.eng.Store().Get().Panel.APIToken
			if subtle.ConstantTimeCompare([]byte(token), []byte(expected)) == 1 {
				if !s.tokenAllowed(r) {
					s.writeError(w, http.StatusForbidden, "该来源不允许使用 API Token，请使用面板登录")
					return
				}
				next(w, r)
				return
			}
		}

		s.writeError(w, http.StatusUnauthorized, "未登录或凭据已失效")
	}
}

// tokenAllowed enforces panel.token_ips so a public deployment does not accept
// the static token from the internet.
func (s *Server) tokenAllowed(r *http.Request) bool {
	cfg := s.eng.Store().Get()
	allowed := parseCIDRs(cfg.Panel.TokenIPs)
	if len(allowed) == 0 {
		return false
	}
	ip := net.ParseIP(s.clientIP(r))
	if ip == nil {
		return false
	}
	for _, network := range allowed {
		if network.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil || !ipInAny(ip, s.trusted) {
		return host
	}
	if forwarded := r.Header.Get("CF-Connecting-IP"); forwarded != "" {
		return strings.TrimSpace(forwarded)
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first := strings.TrimSpace(strings.Split(forwarded, ",")[0])
		if net.ParseIP(first) != nil {
			return first
		}
	}
	return host
}

func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip != nil && ipInAny(ip, s.trusted) {
		return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
	}
	return false
}

// --- helpers -------------------------------------------------------------

func bearerToken(r *http.Request) string {
	header := r.Header.Get("Authorization")
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

func isMutating(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
		return true
	}
	return false
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}

func (s *Server) writeError(w http.ResponseWriter, status int, msg string) {
	s.writeJSON(w, status, map[string]any{"error": msg})
}

func (s *Server) readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	body := http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		s.writeError(w, http.StatusBadRequest, "请求内容无法解析："+err.Error())
		return false
	}
	return true
}

func parseCIDRs(values []string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(values))
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if !strings.Contains(v, "/") {
			if ip := net.ParseIP(v); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				v = fmt.Sprintf("%s/%d", v, bits)
			}
		}
		if _, network, err := net.ParseCIDR(v); err == nil {
			out = append(out, network)
		}
	}
	return out
}

func ipInAny(ip net.IP, networks []*net.IPNet) bool {
	for _, n := range networks {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func randomToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}

// --- rate limiting -------------------------------------------------------

type rateLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attempt
	limit    int
	window   time.Duration
}

type attempt struct {
	count int
	until time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{attempts: map[string]*attempt{}, limit: limit, window: window}
}

// blocked reports whether key is currently locked out.
func (l *rateLimiter) blocked(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok {
		return false, 0
	}
	if time.Now().After(a.until) {
		delete(l.attempts, key)
		return false, 0
	}
	return a.count >= l.limit, time.Until(a.until)
}

// fail records a failed attempt and returns the remaining window.
func (l *rateLimiter) fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, ok := l.attempts[key]
	if !ok || time.Now().After(a.until) {
		a = &attempt{}
		l.attempts[key] = a
	}
	a.count++
	a.until = time.Now().Add(l.window)
}

func (l *rateLimiter) reset(key string) {
	l.mu.Lock()
	delete(l.attempts, key)
	l.mu.Unlock()
}

// versionInfo is what /api/version and /api/health report.
func versionInfo() map[string]any {
	return map[string]any{
		"version": version.Version,
		"commit":  version.Commit,
		"built":   version.Date,
		"xray":    version.Xray(),
		"go":      version.GoRuntime(),
	}
}
