package engine

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ldm0206/vless-to-http/internal/config"
	"github.com/ldm0206/vless-to-http/internal/subscription"
)

// TestRefreshRecordsProviderQuota covers the subscription-userinfo header end
// to end: the panels read what it carries as the remaining traffic.
func TestRefreshRecordsProviderQuota(t *testing.T) {
	header := "upload=1000; download=2000; total=100000; expire=1773203698"
	body := "proxies:\n" +
		"  - name: \"香港01\"\n" +
		"    type: vless\n" +
		"    server: hk.example.com\n" +
		"    port: 443\n" +
		"    uuid: " + testUUID + "\n" +
		"    network: tcp\n"

	served := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if served {
			w.Header().Set("subscription-userinfo", header)
		}
		fmt.Fprint(w, body)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := config.Default()
	cfg.Subscriptions = []config.Subscription{{
		ID: "sub_1", Name: "机场A", URL: srv.URL, Kind: "auto", Enabled: true,
	}}
	store := writeConfig(t, dir, cfg)
	cache := subscription.NewCache(dir)

	eng := New(store, cache, newTestLogger(t), dir)
	defer eng.Close()

	if err := eng.RefreshSubscription(context.Background(), "sub_1"); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if n := len(cache.Nodes("sub_1")); n != 1 {
		t.Fatalf("cached %d nodes, want 1", n)
	}

	sub := store.Get().FindSub("sub_1")
	want := config.SubUserInfo{Upload: 1000, Download: 2000, Total: 100000, Expire: 1773203698}
	if sub.UserInfo != want {
		t.Fatalf("user info = %+v, want %+v", sub.UserInfo, want)
	}
	if used, left := sub.UserInfo.Used(), sub.UserInfo.Remaining(); used != 3000 || left != 97000 {
		t.Fatalf("used = %d, remaining = %d", used, left)
	}

	// A provider that stops reporting must not leave a stale quota behind: the
	// panel would keep showing numbers nobody is maintaining.
	served = false
	if err := eng.RefreshSubscription(context.Background(), "sub_1"); err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if sub := store.Get().FindSub("sub_1"); sub.UserInfo != (config.SubUserInfo{}) {
		t.Fatalf("user info survived a fetch that did not report one: %+v", sub.UserInfo)
	}
}
