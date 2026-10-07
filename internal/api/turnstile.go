package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
)

// turnstileEndpoint is Cloudflare's siteverify API.
const turnstileEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

var turnstileClient = &http.Client{Timeout: 10 * time.Second}

type turnstileResponse struct {
	Success     bool     `json:"success"`
	ErrorCodes  []string `json:"error-codes"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	Action      string   `json:"action"`
}

// verifyTurnstile checks a widget token. The first return value is the verdict;
// the error is reserved for transport failures so the caller can decide
// whether to fail open.
func (s *Server) verifyTurnstile(ctx context.Context, cfg config.Turnstile, token, remoteIP string) (bool, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return false, nil
	}

	form := url.Values{}
	form.Set("secret", cfg.SecretKey)
	form.Set("response", token)
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, turnstileEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := turnstileClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	var parsed turnstileResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return false, fmt.Errorf("解析 Turnstile 响应失败：%w", err)
	}
	if !parsed.Success && len(parsed.ErrorCodes) > 0 {
		return false, fmt.Errorf("Turnstile 拒绝：%s", strings.Join(parsed.ErrorCodes, ","))
	}
	return parsed.Success, nil
}

// subtleEqual compares two strings without leaking length or content through
// timing, used for the admin username check.
func subtleEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
