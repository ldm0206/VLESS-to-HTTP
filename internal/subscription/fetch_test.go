package subscription

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A failed fetch is logged, shown on the panel and persisted as last_error, so
// the credential in the link must not travel with the error.
func TestFetchErrorHidesTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	link := srv.URL + "/sub?token=SECRET-TOKEN&id=42"
	srv.Close() // nothing listens there any more, so the fetch fails

	_, err := Fetch(context.Background(), link, "")
	if err == nil {
		t.Fatal("fetching from a closed server should fail")
	}
	if strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("the error leaks the subscription token: %v", err)
	}
	if !strings.Contains(err.Error(), "请求失败") {
		t.Fatalf("the error should still say what failed: %v", err)
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://user:pw@example.com/sub?token=abc#frag", "https://example.com/sub"},
		{"https://example.com/sub?token=abc", "https://example.com/sub"},
		{"https://example.com/sub", "https://example.com/sub"},
	}
	for _, c := range cases {
		if got := redactURL(c.in); got != c.want {
			t.Errorf("redactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
