package panel

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// 304 时不能把 nil 当成「用户被清空了」。
//
// 这是这次改动里最危险的一处：Users 返回 (nil, false, nil) 表示「没变」，
// 调用方要是不看第二个返回值，就会拿着 nil 去同步，把节点上所有用户都
// 删掉——全站用户在下一个同步周期集体断线，而面板上一切正常。
func TestUsersReportsUnchangedOn304(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.Header().Set("ETag", `"v1"`)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users":[{"id":1,"uuid":"u1","speed_limit":100,"device_limit":2}]}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})

	// 第一次：拿到全量
	users, changed, err := c.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !changed || len(users) != 1 {
		t.Fatalf("首次拉取 changed=%v 用户数=%d，期望 true / 1", changed, len(users))
	}

	// 第二次：应当带上 ETag 换到 304
	users, changed, err = c.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("面板回了 304，却报告列表变过")
	}
	if users != nil {
		t.Errorf("304 时不该返回用户列表，得到 %d 个", len(users))
	}
	if hits != 2 {
		t.Errorf("请求了 %d 次，期望 2 次", hits)
	}
}

// 解析失败时不能记 ETag。
//
// 记了的话下一轮会拿它换回 304，那份没解析成功的列表就永远同步不上了——
// 节点会一直停在更早的那份用户上，而且不再报错。
func TestUsersDoesNotCacheETagOnParseFailure(t *testing.T) {
	var served int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		served++
		if r.Header.Get("If-None-Match") != "" {
			t.Errorf("第 %d 次请求带了 If-None-Match，解析失败后不该记 ETag", served)
		}
		w.Header().Set("ETag", `"bad"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"users": this is not json`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, NodeID: "n1", NodeType: "vless", Token: "t"})
	for i := 0; i < 2; i++ {
		if _, _, err := c.Users(context.Background()); err == nil {
			t.Fatal("坏 JSON 却没报错")
		}
	}
}
