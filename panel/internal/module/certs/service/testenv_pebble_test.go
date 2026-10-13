package service

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/letsencrypt/pebble/v2/ca"
	"github.com/letsencrypt/pebble/v2/db"
	"github.com/letsencrypt/pebble/v2/va"
	"github.com/letsencrypt/pebble/v2/wfe"
)

// fakeCA 是进程内的 pebble（Let's Encrypt 的测试 CA，支持 ARI 与 replaces），外面包一层记账与注错：
// 数请求、记下每张新订单带的 replaces、按需对新订单回 429。
type fakeCA struct {
	srv   *httptest.Server
	dir   string
	roots *x509.CertPool
	store *db.MemoryStore

	mu         sync.Mutex
	requests   int
	newOrders  int
	replaces   []string
	reject429  string // 非空时新订单回 429，值是 Retry-After
	eabKeyID   string
	eabHMACB64 string
}

// startCA 起一个 pebble。eab 为真时要求外部账号绑定（模拟 ZeroSSL）。
func startCA(t *testing.T, dnsAddr string, eab bool) *fakeCA {
	t.Helper()
	// pebble 在构造时读这几个环境变量：不随机睡、不随机拒 nonce、不复用授权（每张都真验一次 DNS）
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")
	t.Setenv("PEBBLE_AUTHZREUSE", "0")
	logger := log.New(io.Discard, "", 0)
	store := db.NewMemoryStore()
	authority := ca.New(logger, store, "", "ecdsa", 0, 1, map[string]ca.Profile{
		"default": {Description: "default"},
	})
	f := &fakeCA{store: store}
	if eab {
		f.eabKeyID = "kid-test-1"
		f.eabHMACB64 = base64.RawURLEncoding.EncodeToString([]byte("zerossl-test-hmac-key-0123456789"))
		if err := store.AddExternalAccountKeyByID(f.eabKeyID, f.eabHMACB64); err != nil {
			t.Fatal(err)
		}
	}
	validator := va.New(logger, 0, 0, false, dnsAddr, store)
	front := wfe.New(logger, store, validator, authority, []string{"pebble.letsencrypt.org"}, false, eab, 0, 0)
	inner := front.Handler()
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		isNewOrder := strings.HasSuffix(r.URL.Path, "/order-plz") && r.Method == http.MethodPost
		reject := f.reject429
		f.mu.Unlock()
		if isNewOrder {
			body, _ := io.ReadAll(r.Body)
			r.Body = io.NopCloser(bytes.NewReader(body))
			if reject != "" {
				w.Header().Set("Content-Type", "application/problem+json")
				w.Header().Set("Retry-After", reject)
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"type":"urn:ietf:params:acme:error:rateLimited","detail":"too many new orders","status":429}`))
				return
			}
			f.mu.Lock()
			f.newOrders++
			f.replaces = append(f.replaces, jwsReplaces(body))
			f.mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.dir = f.srv.URL + wfe.DirectoryPath
	f.roots = x509.NewCertPool()
	f.roots.AddCert(f.srv.Certificate())
	return f
}

// settle 等 pebble 把刚签出的证书登记完再返回。pebble 的 finalize 在另一个协程里：证书先能下载，
// 之后才记下「序列号 → 订单」，带 replaces 的那单还要再把被替换的旧订单标成已替换。签出后紧接着
// 带 replaces 续期，可能撞上旧证书还没登记，pebble 回 500「could not find an order for the given
// certificate」，续期失败（main 上偶发的 sent=[ <id>] 就是它）；同一张旧证书第二次 replaces 也可能
// 因为还没标已替换而被接受。所以每次签出后、下一次带 replaces 之前先 settle。
func (f *fakeCA) settle(t *testing.T, serial, replacedSerial string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		if _, err := f.store.GetOrderByIssuedSerial(serial); err != nil {
			ok = false
		}
		if replacedSerial != "" {
			ord, err := f.store.GetOrderByIssuedSerial(replacedSerial)
			if err != nil {
				ok = false
			} else {
				ord.RLock()
				replaced := ord.IsReplaced
				ord.RUnlock()
				if !replaced {
					ok = false
				}
			}
		}
		if ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pebble 未在时限内登记 serial=%q replaced=%q", serial, replacedSerial)
}

// jwsReplaces 从新订单的 JWS 里取出 payload 的 replaces 字段。
func jwsReplaces(body []byte) string {
	var jws struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal(body, &jws) != nil {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(jws.Payload)
	if err != nil {
		return ""
	}
	var p struct {
		Replaces string `json:"replaces"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Replaces
}

func (f *fakeCA) stats() (requests, newOrders int, replaces []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests, f.newOrders, append([]string(nil), f.replaces...)
}

func (f *fakeCA) set429(retryAfter string) {
	f.mu.Lock()
	f.reject429 = retryAfter
	f.mu.Unlock()
}
