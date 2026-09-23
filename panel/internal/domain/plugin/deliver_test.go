package plugin

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

// 投递的契约测试。
//
// 签名算错了不会有任何人报错 —— 面板照发，插件那边验不过就默默丢弃，
// 表现是「插件收不到事件」，而这个症状指向的地方太多了。所以这里按
// 插件作者会写的方式重新算一遍签名，两边对得上才算数。
//
// 用 devMode=true 构造服务：httptest 起在 127.0.0.1 上，生产模式下
// 那正是被 SSRF 校验挡掉的地址。这不是绕过防线，是这条防线在测试
// 环境里本来就该让路 —— 生产模式的拦截另有用例覆盖。

func TestPost_签名可被插件复算(t *testing.T) {
	const secret = "whsec_test_key"
	type got struct {
		body      []byte
		sig, ts   string
		event     string
		delivery  string
		userAgent string
		ctype     string
	}
	ch := make(chan got, 1)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{
			body:      b,
			sig:       r.Header.Get("X-Pandora-Signature"),
			ts:        r.Header.Get("X-Pandora-Timestamp"),
			event:     r.Header.Get("X-Pandora-Event"),
			delivery:  r.Header.Get("X-Pandora-Delivery"),
			userAgent: r.Header.Get("User-Agent"),
			ctype:     r.Header.Get("Content-Type"),
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := New(nil, nil, true)
	payload := []byte(`{"event":"order.paid","order_id":"abc","amount":1200}`)
	code, err := s.post(context.Background(), dueDelivery{
		id: "deliv-1", event: "order.paid", payload: payload,
		endpoint: srv.URL, timeoutMS: 5000, code: "myplugin",
	}, secret)
	if err != nil {
		t.Fatalf("发送失败: %v", err)
	}
	if code != 200 {
		t.Fatalf("回码 %d，期望 200", code)
	}

	var g got
	select {
	case g = <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("插件端没收到请求")
	}

	if string(g.body) != string(payload) {
		t.Errorf("请求体被改动了\n发出: %s\n收到: %s", payload, g.body)
	}
	if g.event != "order.paid" {
		t.Errorf("X-Pandora-Event = %q", g.event)
	}
	if g.delivery != "deliv-1" {
		t.Errorf("X-Pandora-Delivery = %q", g.delivery)
	}
	if g.ctype != "application/json" {
		t.Errorf("Content-Type = %q", g.ctype)
	}
	if g.userAgent == "" {
		t.Error("没带 User-Agent，对方的日志里会分不清是谁在打")
	}

	// 插件作者按文档会这么算：HMAC-SHA256(secret, "时间戳.请求体")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(g.ts))
	mac.Write([]byte("."))
	mac.Write(g.body)
	want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if g.sig != want {
		t.Errorf("签名对不上\n面板发的: %s\n插件算的: %s", g.sig, want)
	}

	// 时间戳要能用来判新鲜度，不能是个固定值或空串
	ts, err := strconv.ParseInt(g.ts, 10, 64)
	if err != nil {
		t.Fatalf("时间戳不是整数: %q", g.ts)
	}
	if d := time.Since(time.Unix(ts, 0)); d > time.Minute || d < -time.Minute {
		t.Errorf("时间戳偏离当前时间 %v，插件按它做防重放会误判", d)
	}
}

// 换一个字节，签名就该对不上 —— 否则等于没签。
func TestPost_签名覆盖请求体与时间戳(t *testing.T) {
	const secret = "whsec_k"
	var body []byte
	var ts, sig string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		ts = r.Header.Get("X-Pandora-Timestamp")
		sig = r.Header.Get("X-Pandora-Signature")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := New(nil, nil, true)
	if _, err := s.post(context.Background(), dueDelivery{
		id: "d", event: "e", payload: []byte(`{"a":1}`),
		endpoint: srv.URL, timeoutMS: 5000, code: "c",
	}, secret); err != nil {
		t.Fatal(err)
	}

	calc := func(ts string, b []byte) string {
		m := hmac.New(sha256.New, []byte(secret))
		m.Write([]byte(ts))
		m.Write([]byte("."))
		m.Write(b)
		return "sha256=" + hex.EncodeToString(m.Sum(nil))
	}
	if calc(ts, body) != sig {
		t.Fatal("基准就对不上")
	}
	if calc(ts, []byte(`{"a":2}`)) == sig {
		t.Error("改了请求体签名却没变 —— 请求体没进签名")
	}
	if calc("0", body) == sig {
		t.Error("改了时间戳签名却没变 —— 时间戳没进签名，录下的请求可以无限重放")
	}
}

// 没配密钥时不该带一个假的签名头：插件看到有签名就会去验，
// 验不过又收不到东西，比明确「这条没签名」更难查。
func TestPost_无密钥不带签名头(t *testing.T) {
	var has bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, has = r.Header["X-Pandora-Signature"]
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := New(nil, nil, true)
	if _, err := s.post(context.Background(), dueDelivery{
		id: "d", event: "e", payload: []byte(`{}`),
		endpoint: srv.URL, timeoutMS: 5000, code: "c",
	}, ""); err != nil {
		t.Fatal(err)
	}
	if has {
		t.Error("没有密钥却发了签名头")
	}
}

// 不跟随跳转：一个通过校验的外网地址可以 302 到 169.254.169.254。
func TestPost_不跟随跳转(t *testing.T) {
	reached := false
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.WriteHeader(200)
	}))
	defer inner.Close()
	outer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, inner.URL, http.StatusFound)
	}))
	defer outer.Close()

	s := New(nil, nil, true)
	code, err := s.post(context.Background(), dueDelivery{
		id: "d", event: "e", payload: []byte(`{}`),
		endpoint: outer.URL, timeoutMS: 5000, code: "c",
	}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if reached {
		t.Error("跟着 302 跳过去了 —— 地址校验形同虚设")
	}
	if code != http.StatusFound {
		t.Errorf("回码 %d，期望原样返回 302", code)
	}
}

// 超时要真的生效：一个不返回的插件不能把派发器卡住。
func TestPost_超时(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := New(nil, nil, true)
	start := time.Now()
	_, err := s.post(context.Background(), dueDelivery{
		id: "d", event: "e", payload: []byte(`{}`),
		endpoint: srv.URL, timeoutMS: 300, code: "c",
	}, "k")
	if err == nil {
		t.Fatal("超时了却没报错")
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("等了 %v 才放弃，超时设的是 300ms", d)
	}
}

// 生产模式下内网地址在发送这一步也要再挡一次（防 DNS 重绑定）。
func TestPost_生产模式挡内网(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("生产模式下不该真的发出去")
		w.WriteHeader(200)
	}))
	defer srv.Close()

	s := New(nil, nil, false) // devMode=false
	if _, err := s.post(context.Background(), dueDelivery{
		id: "d", event: "e", payload: []byte(`{}`),
		endpoint: srv.URL, timeoutMS: 5000, code: "c",
	}, "k"); err == nil {
		t.Error("生产模式下发到 127.0.0.1 却没被拦")
	}
}

func TestKnownEvent(t *testing.T) {
	if !KnownEvent("order.paid") {
		t.Error("order.paid 应当是已知事件")
	}
	if KnownEvent("order.refunded") {
		t.Error("没实现退款，order.refunded 不该被当成已知事件")
	}
	if KnownEvent("") {
		t.Error("空事件名不该通过")
	}
}
