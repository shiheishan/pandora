// [INPUT]: 依赖 net/http/httptest 起假网关，依赖 domain/subscription 的 DetectFormat 决定假订阅回哪种 Content-Type，依赖 ltkit 的 Manifest
// [OUTPUT]: 对外提供 测试用的 fakePanel（public 与 admin 两个假网关、请求记录与 burst 用的用户状态）与 testManifest
// [POS]: tools/loadtest/userload 测试的假面板：只模拟 users / burst 用到的路由与形状，顺带核对每个请求的 X-Real-IP 是否是该用户固定的地址

package userload

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

const (
	fakePrefix   = "a1b2c3d4e5f6"
	fakePassword = "fictitious-portal-pass"
	fakeAdmin    = "ops@loadtest.invalid"
	fakeAdminPW  = "fictitious-admin-pass"
	fakeAdminIP  = "198.51.100.77"
)

// testManifest 造 n 个虚构用户：每人一个独立 /24 的文档网段地址。
func testManifest(n int) *ltkit.Manifest {
	m := &ltkit.Manifest{Version: ltkit.ManifestVersion, Label: "test", UserPassword: fakePassword}
	for i := range n {
		m.Users = append(m.Users, ltkit.ManifestUser{
			ID:             fmt.Sprintf("00000000-0000-4000-8000-%012d", i),
			Email:          fmt.Sprintf("lt%05d@loadtest.invalid", i),
			SubscribeToken: fmt.Sprintf("subtoken-%08d-xxxxxxxxxxxxxxxx", i),
			RealIP:         fmt.Sprintf("10.%d.%d.10", i/256, i%256),
		})
	}
	return m
}

type seenReq struct {
	gw, method, path, ip, cf, auth, idem, ctype, ua string
	body                                            map[string]any
}

type fakePanel struct {
	t   *testing.T
	pub *httptest.Server
	adm *httptest.Server

	mu         sync.Mutex
	byEmail    map[string]ltkit.ManifestUser
	byToken    map[string]ltkit.ManifestUser
	sessions   map[string]string // 门户 Bearer → 邮箱
	logins     map[string]int
	violations []string
	reqs       []seenReq
	nextTok    int

	// 行为开关
	slow          time.Duration
	fail500Portal bool // 门户读接口（除 /v1/me/subscriptions）一律 500
	rlPortal      bool // 门户读接口回中间件的 429 rate_limited
	rlSub         bool // 订阅回纯文本 429

	// burst 的用户状态
	groupID      *string
	override     *int
	planMax      *int
	groups       []map[string]string
	reauthOnce   bool
	adminToken   string
	groupCreates int
}

func newFakePanel(t *testing.T, m *ltkit.Manifest) *fakePanel {
	f := &fakePanel{t: t, byEmail: map[string]ltkit.ManifestUser{}, byToken: map[string]ltkit.ManifestUser{},
		sessions: map[string]string{}, logins: map[string]int{}, adminToken: "adm-1"}
	for _, u := range m.Users {
		f.byEmail[u.Email] = u
		f.byToken[u.SubscribeToken] = u
	}
	f.pub = httptest.NewServer(http.HandlerFunc(f.public))
	f.adm = httptest.NewServer(http.HandlerFunc(f.admin))
	t.Cleanup(func() { f.pub.Close(); f.adm.Close() })
	return f
}

// set 在锁里改行为开关：假网关的处理协程与测试协程之间只隔着网络，竞态检测器看不到那条先后关系。
func (f *fakePanel) set(fn func()) {
	f.mu.Lock()
	fn()
	f.mu.Unlock()
}

func (f *fakePanel) record(gw string, r *http.Request) map[string]any {
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, seenReq{gw: gw, method: r.Method, path: r.URL.RequestURI(), ip: r.Header.Get("X-Real-IP"), cf: r.Header.Get("CF-Connecting-IP"),
		auth: r.Header.Get("Authorization"), idem: r.Header.Get("Idempotency-Key"),
		ctype: r.Header.Get("Content-Type"), ua: r.UserAgent(), body: body})
	f.mu.Unlock()
	return body
}

func (f *fakePanel) violate(format string, args ...any) {
	f.mu.Lock()
	f.violations = append(f.violations, fmt.Sprintf(format, args...))
	f.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": code}})
}

// ---------------------------------------------------------------------------
// public
// ---------------------------------------------------------------------------

func (f *fakePanel) public(w http.ResponseWriter, r *http.Request) {
	body := f.record("public", r)
	f.mu.Lock()
	slow, rlPortal, fail500, rlSub := f.slow, f.rlPortal, f.fail500Portal, f.rlSub
	f.mu.Unlock()
	if slow > 0 {
		time.Sleep(slow)
	}
	ip := r.Header.Get("X-Real-IP")
	path := r.URL.Path
	switch {
	case r.Method == http.MethodPost && path == "/v1/auth/login":
		email, _ := body["email"].(string)
		u, ok := f.byEmail[email]
		if !ok || body["password"] != fakePassword {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		if ip != u.RealIP {
			f.violate("login %s from %q, want %q", email, ip, u.RealIP)
		}
		f.mu.Lock()
		f.nextTok++
		tok := fmt.Sprintf("pt-%d", f.nextTok)
		f.sessions[tok] = email
		f.logins[email]++
		f.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"access_token": tok, "token_type": "Bearer", "expires_in": 3600})
	case strings.HasPrefix(path, "/v1/"):
		f.mu.Lock()
		email, ok := f.sessions[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		f.mu.Unlock()
		if !ok {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		u := f.byEmail[email]
		if ip != u.RealIP {
			f.violate("%s by %s from %q, want %q", path, email, ip, u.RealIP)
		}
		switch {
		case path == "/v1/me/subscriptions":
			writeJSON(w, http.StatusOK, map[string]any{"subscriptions": []map[string]string{{"id": "sub-" + u.ID}}})
		case path == "/v1/me/subscription-links":
			writeJSON(w, http.StatusOK, map[string]any{"links": []map[string]string{
				{"url": "https://panel.example.invalid/" + fakePrefix + "/" + u.SubscribeToken}}})
		case rlPortal:
			writeErr(w, http.StatusTooManyRequests, "rate_limited")
		case fail500:
			writeErr(w, http.StatusInternalServerError, "internal")
		default:
			writeJSON(w, http.StatusOK, map[string]any{})
		}
	default:
		f.subscribe(w, r, ip, rlSub)
	}
}

func (f *fakePanel) subscribe(w http.ResponseWriter, r *http.Request, ip string, rlSub bool) {
	seg := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	u, ok := f.byToken[seg[len(seg)-1]]
	if len(seg) != 2 || seg[0] != fakePrefix || !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if ip != u.RealIP {
		f.violate("subscription of %s from %q, want %q", u.Email, ip, u.RealIP)
	}
	if rlSub {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	ct := map[subscription.Format]string{
		subscription.FormatClash:   "text/yaml; charset=utf-8",
		subscription.FormatSingbox: "application/json; charset=utf-8",
		subscription.FormatURI:     "text/plain; charset=utf-8",
	}[subscription.DetectFormat(r.UserAgent(), "")]
	w.Header().Set("Content-Type", ct)
	_, _ = w.Write([]byte("body"))
}

// ---------------------------------------------------------------------------
// admin
// ---------------------------------------------------------------------------

func (f *fakePanel) admin(w http.ResponseWriter, r *http.Request) {
	body := f.record("admin", r)
	if ip := r.Header.Get("X-Real-IP"); ip != fakeAdminIP {
		f.violate("admin %s from %q", r.URL.Path, ip)
	}
	path := r.URL.Path
	if r.Method == http.MethodPost && path == "/v1/auth/login" {
		if body["email"] != fakeAdmin || body["password"] != fakeAdminPW {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "adm-1"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+f.adminToken {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	switch {
	case r.Method == http.MethodPost && path == "/v1/auth/reauth":
		if body["password"] != fakeAdminPW {
			writeErr(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		f.adminToken = "adm-2"
		writeJSON(w, http.StatusOK, map[string]any{"access_token": "adm-2"})
	case r.Method == http.MethodGet && path == "/v1/user-groups":
		writeJSON(w, http.StatusOK, map[string]any{"groups": f.groups})
	case r.Method == http.MethodPost && path == "/v1/user-groups":
		f.groupCreates++
		id := fmt.Sprintf("group-%d", f.groupCreates)
		f.groups = append(f.groups, map[string]string{"id": id, "code": body["code"].(string)})
		writeJSON(w, http.StatusOK, map[string]any{"id": id})
	case r.Method == http.MethodGet && strings.HasPrefix(path, "/v1/users/"):
		writeJSON(w, http.StatusOK, map[string]any{
			"id": strings.TrimPrefix(path, "/v1/users/"), "group_id": f.groupID,
			"subscriptions": []map[string]any{
				{"id": "sub-old", "status": "expired", "device_limit_override": nil, "plan_max_devices": 1},
				{"id": "sub-cur", "status": "active", "device_limit_override": f.override, "plan_max_devices": f.planMax},
			},
		})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/device-limit"):
		if v, ok := body["limit"].(float64); ok {
			n := int(v)
			f.override = &n
		} else {
			f.override = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	case r.Method == http.MethodPost && strings.HasSuffix(path, "/group"):
		if f.reauthOnce {
			f.reauthOnce = false
			writeErr(w, http.StatusForbidden, "reauth_required")
			return
		}
		if g, _ := body["group_id"].(string); g != "" {
			f.groupID = &g
		} else {
			f.groupID = nil
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	default:
		writeJSON(w, http.StatusOK, map[string]any{})
	}
}

func (f *fakePanel) requests(gw, pathPrefix string) []seenReq {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []seenReq
	for _, r := range f.reqs {
		if r.gw == gw && strings.HasPrefix(r.path, pathPrefix) {
			out = append(out, r)
		}
	}
	return out
}
