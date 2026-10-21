package certs

import (
	"bytes"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"log/slog"
	"net"
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
	"github.com/miekg/dns"
)

// testCA 是进程内的 pebble，外面包一层记账与注错。
type testCA struct {
	srv   *httptest.Server
	dir   string // 目录 URL：srv.URL + wfe.DirectoryPath
	roots *x509.CertPool
	store *db.MemoryStore
	// httpPort 是 pebble VA 访问标识符 HTTP-01 时连的端口。
	httpPort int
	// managerHTTPPort 非 0 时 Manager 监听该端口；为 0 时与 httpPort 相同（见 TestManagerValidationFailedBacksOff）。
	managerHTTPPort int

	mu        sync.Mutex
	newOrders int
	replaces  []string // 每张新订单 payload 里的 replaces（没有记 "")
	profiles  []string // 每张新订单 payload 里的 profile（没有记 "")
	reject429 string   // 非空时新订单回 429，值是 Retry-After 秒数
}

// startTestCA 起 pebble。withShortlived 为假时不配 shortlived 档；dnsAddr 为空时 VA 用系统解析。
func startTestCA(t *testing.T, withShortlived bool, dnsAddr string) *testCA {
	t.Helper()
	t.Setenv("PEBBLE_VA_NOSLEEP", "1")
	t.Setenv("PEBBLE_WFE_NONCEREJECT", "0")
	t.Setenv("PEBBLE_AUTHZREUSE", "0")

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpPort := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	logger := log.New(io.Discard, "", 0)
	store := db.NewMemoryStore()

	profiles := map[string]ca.Profile{
		"classic": {
			Description:    "classic",
			ValidityPeriod: uint64(90 * 24 * time.Hour / time.Second),
		},
	}
	if withShortlived {
		profiles["shortlived"] = ca.Profile{
			Description:    "shortlived",
			ValidityPeriod: uint64(160 * time.Hour / time.Second),
		}
	}

	authority := ca.New(logger, store, "", "ecdsa", 0, 1, profiles)
	validator := va.New(logger, httpPort, 0, false, dnsAddr, store)
	front := wfe.New(logger, store, validator, authority, []string{"pebble.letsencrypt.org"}, false, false, 0, 0)
	inner := front.Handler()

	c := &testCA{
		store:    store,
		httpPort: httpPort,
	}
	c.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isNewOrder := strings.HasSuffix(r.URL.Path, "/order-plz") && r.Method == http.MethodPost
		c.mu.Lock()
		reject := c.reject429
		c.mu.Unlock()

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
			repl, prof := jwsOrderFields(body)
			c.mu.Lock()
			c.newOrders++
			c.replaces = append(c.replaces, repl)
			c.profiles = append(c.profiles, prof)
			c.mu.Unlock()
		}
		inner.ServeHTTP(w, r)
	}))
	t.Cleanup(c.srv.Close)
	c.dir = c.srv.URL + wfe.DirectoryPath
	c.roots = x509.NewCertPool()
	c.roots.AddCert(c.srv.Certificate())
	return c
}

func (c *testCA) stats() (newOrders int, replaces, profiles []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.newOrders, append([]string(nil), c.replaces...), append([]string(nil), c.profiles...)
}

func (c *testCA) set429(retryAfterSeconds string) {
	c.mu.Lock()
	c.reject429 = retryAfterSeconds
	c.mu.Unlock()
}

// settle 等 pebble 把刚签出的证书登记完再返回（见旧集成测试注释）。
func (c *testCA) settle(t *testing.T, serial, replacedSerial string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		if _, err := c.store.GetOrderByIssuedSerial(serial); err != nil {
			ok = false
		}
		if replacedSerial != "" {
			ord, err := c.store.GetOrderByIssuedSerial(replacedSerial)
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

func jwsOrderFields(body []byte) (replaces, profile string) {
	var jws struct {
		Payload string `json:"payload"`
	}
	if json.Unmarshal(body, &jws) != nil {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(jws.Payload)
	if err != nil {
		return "", ""
	}
	var p struct {
		Replaces string `json:"replaces"`
		Profile  string `json:"profile"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Replaces, p.Profile
}

// serialHex 与 pebble db 里 ordersByIssuedSerial 的键一致：证书序列号字节的十六进制小写。
func serialHex(leaf *x509.Certificate) string {
	return hex.EncodeToString(leaf.SerialNumber.Bytes())
}

func newTestManager(t *testing.T, ca *testCA, dir string, clock *fakeClock) *Manager {
	t.Helper()
	port := ca.httpPort
	if ca.managerHTTPPort != 0 {
		port = ca.managerHTTPPort
	}
	m, err := New(Options{
		Dir:          dir,
		DirectoryURL: ca.dir,
		HTTPPort:     port,
		RootCAs:      ca.roots,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:          clock.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Now()}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

// fakeDNS 是进程内权威 DNS，只答 A 记录；pebble VA 经 TCP 查询。
type fakeDNS struct {
	mu      sync.Mutex
	a       map[string]string // fqdn（小写、无末尾点）→ IPv4 文本
	address string
}

func startFakeDNS(t *testing.T) *fakeDNS {
	t.Helper()
	f := &fakeDNS{a: map[string]string{}}
	var (
		pc  net.PacketConn
		ln  net.Listener
		err error
	)
	for i := 0; i < 20; i++ {
		if pc, err = net.ListenPacket("udp", "127.0.0.1:0"); err != nil {
			t.Fatal(err)
		}
		if ln, err = net.Listen("tcp", pc.LocalAddr().String()); err == nil {
			break
		}
		_ = pc.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	f.address = pc.LocalAddr().String()
	udp := &dns.Server{PacketConn: pc, Handler: f}
	tcp := &dns.Server{Listener: ln, Handler: f}
	go func() { _ = udp.ActivateAndServe() }()
	go func() { _ = tcp.ActivateAndServe() }()
	t.Cleanup(func() { _ = udp.Shutdown(); _ = tcp.Shutdown() })
	return f
}

func (f *fakeDNS) setA(name, ip string) {
	f.mu.Lock()
	name = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
	f.a[name] = ip
	f.mu.Unlock()
}

func (f *fakeDNS) addr() string { return f.address }

func (f *fakeDNS) ServeDNS(w dns.ResponseWriter, req *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(req)
	m.Authoritative = true
	if len(req.Question) != 1 {
		_ = w.WriteMsg(m)
		return
	}
	q := req.Question[0]
	name := strings.TrimSuffix(strings.ToLower(q.Name), ".")
	f.mu.Lock()
	if q.Qtype == dns.TypeA {
		if ipStr, ok := f.a[name]; ok {
			if ip := net.ParseIP(ipStr); ip != nil && ip.To4() != nil {
				m.Answer = append(m.Answer, &dns.A{
					Hdr: dns.RR_Header{Name: q.Name, Rrtype: dns.TypeA, Class: dns.ClassINET, Ttl: 60},
					A:   ip.To4(),
				})
			}
		}
	}
	f.mu.Unlock()
	_ = w.WriteMsg(m)
}

func useIdentityJitter(t *testing.T) {
	old := jitter
	jitter = func(d time.Duration) time.Duration { return d }
	t.Cleanup(func() { jitter = old })
}
