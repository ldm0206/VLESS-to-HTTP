package subscription

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/ldm0206/vless-to-http/internal/config"
)

// DefaultUserAgent looks like a Clash client so providers hand back the YAML
// dialect, which carries more information than the link list.
const DefaultUserAgent = "clash-verge/v2.4.2"

// maxBody caps how much of a subscription we read; node lists are small and
// an unbounded read would let a hostile URL exhaust memory.
const maxBody = 16 << 20

var client = &http.Client{
	Timeout: 60 * time.Second,
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          8,
		IdleConnTimeout:       60 * time.Second,
		ForceAttemptHTTP2:     true,
	},
}

// redactURL keeps a subscription link readable in an error without exposing the
// credential it carries: providers put the access token in the query string,
// and Go's own url.Error strips only the password inside the userinfo.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "<链接无法解析>"
	}
	u.User = nil
	u.RawQuery = ""
	u.Fragment = ""
	u.RawFragment = ""
	return u.String()
}

// stripURL unwraps the *url.Error a client failure carries, so an error that
// reaches the log or the persisted last_error no longer quotes the full URL.
// The cause is kept, which keeps errors.Is working for callers.
func stripURL(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) && uerr.Err != nil {
		return uerr.Err
	}
	return err
}

// Payload is a downloaded subscription: the node list plus whatever the
// provider reported about the account in its response headers.
type Payload struct {
	Body     []byte
	UserInfo config.SubUserInfo
}

// Fetch downloads a subscription URL.
func Fetch(ctx context.Context, url, userAgent string) (Payload, error) {
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Payload{}, fmt.Errorf("构造请求失败：%s：%w", redactURL(url), stripURL(err))
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return Payload{}, fmt.Errorf("请求失败：%s：%w", redactURL(url), stripURL(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Payload{}, fmt.Errorf("服务端返回 %s", resp.Status)
	}

	// The quota rides along with the node list. A provider that sends no header
	// leaves this empty, which the panel shows as "not reported".
	userInfo, _ := config.ParseSubUserInfo(resp.Header.Get("subscription-userinfo"))

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return Payload{}, fmt.Errorf("读取响应失败：%w", err)
	}
	if len(body) == 0 {
		return Payload{}, fmt.Errorf("订阅返回了空内容")
	}
	return Payload{Body: body, UserInfo: userInfo}, nil
}
