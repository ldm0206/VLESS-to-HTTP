package subscription

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
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

// Fetch downloads a subscription URL and returns the raw body.
func Fetch(ctx context.Context, url, userAgent string) ([]byte, error) {
	if userAgent == "" {
		userAgent = DefaultUserAgent
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("构造请求失败：%w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("服务端返回 %s", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, fmt.Errorf("读取响应失败：%w", err)
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("订阅返回了空内容")
	}
	return body, nil
}
