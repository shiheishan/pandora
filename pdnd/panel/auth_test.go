package panel

import (
	"context"
	"net/http"
	"net/url"
	"testing"
)

func TestNativeClientKeepsRuntimeTokenOutOfURL(t *testing.T) {
	c := New(Options{
		BaseURL: "https://panel.invalid", NodeID: "node-1", NodeType: "vless", Token: "runtime-secret",
	})
	req, err := c.newRequest(context.Background(), http.MethodGet, "user", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer runtime-secret" {
		t.Fatalf("authorization = %q", got)
	}
	query, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	if query.Get("token") != "" || req.URL.Query().Has("token") {
		t.Fatalf("runtime token leaked into URL: %s", req.URL.Redacted())
	}
	if query.Get("node_id") != "node-1" || query.Get("node_type") != "vless" {
		t.Fatalf("node routing query changed: %v", query)
	}
}
