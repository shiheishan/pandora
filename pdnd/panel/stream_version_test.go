package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 建事件流时报上手上的用户名单版本（与 /user 的 ETag 同源，去掉 nginx 加的 W/），
// 面板据此跳过首个全量；还没有版本时不带这个头。
func TestStreamReportsUsersVersion(t *testing.T) {
	for _, tc := range []struct{ etag, want string }{
		{"", ""},
		{`"u1-abc"`, `"u1-abc"`},
		{`W/"u1-abc"`, `"u1-abc"`},
	} {
		got := make(chan string, 1)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case got <- r.Header.Get(StreamUsersVersionHeader):
			default:
			}
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		}))
		c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
		c.SetUsersVersion(tc.etag)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		go c.Stream(ctx, make(chan StreamEvent, 1), nil)
		select {
		case v := <-got:
			if v != tc.want {
				t.Errorf("ETag %q：%s = %q，期望 %q", tc.etag, StreamUsersVersionHeader, v, tc.want)
			}
		case <-ctx.Done():
			t.Fatalf("ETag %q：事件流没有连上来", tc.etag)
		}
		cancel()
		srv.Close()
	}
}
