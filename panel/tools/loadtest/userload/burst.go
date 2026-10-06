package userload

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// ---------------------------------------------------------------------------
// 选的写操作与为什么
// ---------------------------------------------------------------------------
//
// 后台里改「一个用户」并让节点立刻重拉用户表的写操作只有一个：换用户组
// POST /v1/users/{id}/group（api/admin/usergroup.go assignUserGroup）。它提交后调
// notifyNodeUsersChanged → nodefabric.NotifyUsersChanged，往 Redis 的 rt:<tenant>:nodes
// 发一条不带 node_id 的 node.users.changed；持有节点长连接的进程（WatchNodeChanges）收到后
// 对本进程上该租户的每个节点 pushNodeSnapshot：重查节点、分流、配置并 ListNodeUsers，
// 用户集合版本变了就推全量 sync_users（previous 传 nil，不发增量）。封禁、改状态、改设备数
// 这些写操作都不发这条信号，节点只能等下一轮轮询。
//
// 换组本身只有在节点池按用户组限定时才改变用户表；为了让每个节点的用户表真的变（ETag 失效、
// 节点收到全量 sync_users），默认先改这个用户当前订阅的设备数覆盖
// POST /v1/subscriptions/{id}/device-limit（ProxyUser.DeviceLimit 进 UserSetVersion 的摘要），
// 再换组触发推送。两步都来回切换：设备数在「套餐规定（null）」与「一个不同的值」之间，
// 用户组在「无」与 -group-code 指定的压测分组之间，跑偶数次回到原状，重复执行不累积脏数据。
//
// 两个接口在后台前端都是直接 api.post（users/tabs.tsx），不带幂等键、不要求近期重认证；
// 仍按前端客户端的做法处理 403 reauth_required：重认证一次、原样重放。

const (
	opBoth        = "both"
	opGroup       = "group"
	opDeviceLimit = "device-limit"
)

type burstConfig struct {
	manifest  string
	adminURL  string
	admin     credentials
	adminIP   string
	userEmail string
	op        string
	groupCode string
	count     int
	interval  time.Duration
	timeout   time.Duration
	out       string
	ipHeaders []string
}

// BurstMain 是 burst 子命令：go run ./tools/loadtest burst -manifest ... -admin-url ...
func BurstMain(args []string) error {
	cfg, err := parseBurstFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	m, err := ltkit.LoadManifest(cfg.manifest)
	if err != nil {
		return err
	}
	ctx, stop := signalContext()
	defer stop()
	_, err = runBurst(ctx, cfg, m, os.Stdout)
	return err
}

func parseBurstFlags(args []string) (burstConfig, error) {
	var cfg burstConfig
	fs := flag.NewFlagSet("burst", flag.ContinueOnError)
	fs.StringVar(&cfg.manifest, "manifest", "", "manifest written by seed (required)")
	fs.StringVar(&cfg.adminURL, "admin-url", "", "admin gateway base URL including any secret path prefix (required)")
	fs.StringVar(&cfg.admin.email, "admin-email", "", "admin email (default $"+envAdminEmail+")")
	fs.StringVar(&cfg.admin.password, "admin-password", "", "admin password (prefer $"+envAdminPassword+")")
	fs.StringVar(&cfg.adminIP, "admin-ip", defaultAdminIP, "fictitious source IP for the admin requests")
	ipHeaders := fs.String("ip-headers", defaultIPHeader, ipHeadersUsage)
	fs.StringVar(&cfg.userEmail, "user-email", "", "manifest user to change (default: the first manifest user)")
	fs.StringVar(&cfg.op, "op", opBoth, "both | group | device-limit (see burst.go for what each triggers)")
	fs.StringVar(&cfg.groupCode, "group-code", "loadtest-burst", "user group toggled in and out; created once if missing")
	fs.IntVar(&cfg.count, "count", 1, "number of triggers")
	fs.DurationVar(&cfg.interval, "interval", 30*time.Second, "pause between triggers")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "per-request timeout")
	fs.StringVar(&cfg.out, "out", "loadtest-out", "directory for burst.json and burst-http.json/.txt")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.admin.email == "" {
		cfg.admin.email = os.Getenv(envAdminEmail)
	}
	if cfg.admin.password == "" {
		cfg.admin.password = os.Getenv(envAdminPassword)
	}
	cfg.ipHeaders = splitList(*ipHeaders)
	switch {
	case len(cfg.ipHeaders) == 0:
		return cfg, errors.New("burst: -ip-headers is empty")
	case cfg.manifest == "" || cfg.adminURL == "":
		return cfg, errors.New("burst: -manifest and -admin-url are required")
	case cfg.admin.email == "" || cfg.admin.password == "":
		return cfg, fmt.Errorf("burst: needs -admin-email/-admin-password or $%s/$%s", envAdminEmail, envAdminPassword)
	case cfg.op != opBoth && cfg.op != opGroup && cfg.op != opDeviceLimit:
		return cfg, fmt.Errorf("burst: unknown -op %q", cfg.op)
	case cfg.count < 1:
		return cfg, errors.New("burst: -count must be >= 1")
	}
	return cfg, nil
}

// ---------------------------------------------------------------------------
// burst.json
// ---------------------------------------------------------------------------

type burstFile struct {
	Version  int            `json:"version"`
	Label    string         `json:"label"`
	Op       string         `json:"op"`
	AdminIP  string         `json:"admin_ip"`
	UserID   string         `json:"user_id"`
	Email    string         `json:"user_email"`
	Group    *burstGroup    `json:"group,omitempty"`
	Triggers []burstTrigger `json:"triggers"`
}

type burstGroup struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Created bool   `json:"created"`
}

// burstTrigger 是一次触发。TriggerAt 是触发推送的那次写（换组；只改设备数时是那次写）发出的时刻，
// 面板在写成功、回响应之前发 node.users.changed，所以节点侧的尖峰落在 [trigger_at, done_at] 之后。
type burstTrigger struct {
	Seq         int          `json:"seq"`
	TriggerAt   time.Time    `json:"trigger_at"`
	TriggerUnix int64        `json:"trigger_unix_ms"`
	DoneAt      time.Time    `json:"done_at"`
	Push        string       `json:"push"`
	Writes      []burstWrite `json:"writes"`
	OK          bool         `json:"ok"`
}

type burstWrite struct {
	Endpoint       string    `json:"endpoint"`
	Field          string    `json:"field"`
	SubscriptionID string    `json:"subscription_id,omitempty"`
	From           any       `json:"from"`
	To             any       `json:"to"`
	EffectiveFrom  *int      `json:"effective_from,omitempty"`
	EffectiveTo    *int      `json:"effective_to,omitempty"`
	SentAt         time.Time `json:"sent_at"`
	DoneAt         time.Time `json:"done_at"`
	LatencyMS      float64   `json:"latency_ms"`
	Status         int       `json:"status"`
	ErrorCode      string    `json:"error_code,omitempty"`
	Reauthed       bool      `json:"reauthed,omitempty"`
}

// ---------------------------------------------------------------------------
// 运行
// ---------------------------------------------------------------------------

type burster struct {
	cfg   burstConfig
	c     *client
	rec   *ltkit.Recorder
	gw    *gateway
	token string
}

func runBurst(ctx context.Context, cfg burstConfig, m *ltkit.Manifest, stdout io.Writer) (*burstFile, error) {
	user, err := burstUser(m, cfg.userEmail)
	if err != nil {
		return nil, err
	}
	b := &burster{cfg: cfg, c: newClient(4, cfg.timeout, cfg.ipHeaders), rec: ltkit.NewRecorder("burst-http", time.Second),
		gw: newGateway("admin", cfg.adminURL)}
	out := &burstFile{Version: 1, Label: m.Label, Op: cfg.op, AdminIP: cfg.adminIP, UserID: user.ID, Email: user.Email}

	tok, resp := b.c.login(ctx, b.rec, b.gw, cfg.admin, cfg.adminIP)
	if tok == "" {
		return nil, fmt.Errorf("burst: admin login failed: %s", describe(resp))
	}
	b.token = tok
	if cfg.op != opDeviceLimit {
		if out.Group, err = b.ensureGroup(ctx); err != nil {
			return nil, err
		}
	}

	var runErr error
	for i := 1; i <= cfg.count && runErr == nil; i++ {
		if i > 1 {
			select {
			case <-ctx.Done():
				runErr = ctx.Err()
				continue
			case <-time.After(cfg.interval):
			}
		}
		tr, err := b.trigger(ctx, i, user.ID, out.Group)
		if tr != nil {
			out.Triggers = append(out.Triggers, *tr)
			fmt.Fprintf(stdout, "[burst] #%d at %s: %s\n", i, tr.TriggerAt.Format(time.RFC3339Nano), summarizeWrites(tr.Writes))
		}
		runErr = err
	}

	if err := writeBurstFiles(cfg.out, out, b.rec); err != nil {
		return out, err
	}
	fmt.Fprintf(stdout, "[burst] wrote %s\n", filepath.Join(cfg.out, "burst.json"))
	return out, runErr
}

func burstUser(m *ltkit.Manifest, email string) (ltkit.ManifestUser, error) {
	if len(m.Users) == 0 {
		return ltkit.ManifestUser{}, errors.New("burst: manifest has no users")
	}
	if email == "" {
		return m.Users[0], nil
	}
	for _, u := range m.Users {
		if u.Email == email {
			return u, nil
		}
	}
	return ltkit.ManifestUser{}, fmt.Errorf("burst: %s is not in the manifest", email)
}

func writeBurstFiles(dir string, out *burstFile, rec *ltkit.Recorder) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "burst.json"), body, 0o644); err != nil {
		return err
	}
	_, err = rec.WriteFiles(dir)
	return err
}

func summarizeWrites(ws []burstWrite) string {
	s := ""
	for _, w := range ws {
		s += fmt.Sprintf("%s %s→%s HTTP %d %.0fms; ", w.Field, jsonText(w.From), jsonText(w.To), w.Status, w.LatencyMS)
	}
	return s
}

// call 发一个后台请求；403 reauth_required 时重认证一次并以同一个幂等键重放（前端 core/api.ts 的做法）。
func (b *burster) call(ctx context.Context, rq request) (response, bool) {
	rq.gw, rq.ip, rq.ua, rq.token = b.gw, b.cfg.adminIP, browserUA, b.token
	resp := b.c.do(ctx, b.rec, rq)
	if resp.status != http.StatusForbidden || resp.code != "reauth_required" {
		return resp, false
	}
	tok, rr := b.c.reauth(ctx, b.rec, b.gw, b.token, b.cfg.admin.password, b.cfg.adminIP)
	if tok == "" {
		return rr, true
	}
	b.token, rq.token = tok, tok
	return b.c.do(ctx, b.rec, rq), true
}

// ensureGroup 找到 -group-code 指定的分组，没有就建一个（只建一次，之后重复执行都复用）。
func (b *burster) ensureGroup(ctx context.Context) (*burstGroup, error) {
	resp, _ := b.call(ctx, request{method: http.MethodGet, path: "/v1/user-groups", tmpl: "/v1/user-groups"})
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("burst: listing user groups failed: %s", describe(resp))
	}
	var list struct {
		Groups []struct {
			ID   string `json:"id"`
			Code string `json:"code"`
		} `json:"groups"`
	}
	if err := json.Unmarshal(resp.body, &list); err != nil {
		return nil, fmt.Errorf("burst: decode user groups: %w", err)
	}
	for _, g := range list.Groups {
		if g.Code == b.cfg.groupCode {
			return &burstGroup{ID: g.ID, Code: g.Code}, nil
		}
	}
	resp, _ = b.call(ctx, request{method: http.MethodPost, path: "/v1/user-groups", tmpl: "/v1/user-groups",
		body: map[string]string{"code": b.cfg.groupCode, "name": "loadtest burst", "description": "压测 burst 来回切换用户组用，可删除"}})
	var created struct {
		ID string `json:"id"`
	}
	if resp.status != http.StatusOK || json.Unmarshal(resp.body, &created) != nil || created.ID == "" {
		return nil, fmt.Errorf("burst: creating user group %q failed: %s", b.cfg.groupCode, describe(resp))
	}
	return &burstGroup{ID: created.ID, Code: b.cfg.groupCode, Created: true}, nil
}

// userDetail 是 GET /v1/users/{id} 里 burst 用得到的字段（adminops/users.go）。
type userDetail struct {
	GroupID       *string `json:"group_id"`
	Subscriptions []struct {
		ID                  string `json:"id"`
		Status              string `json:"status"`
		DeviceLimitOverride *int   `json:"device_limit_override"`
		PlanMaxDevices      *int   `json:"plan_max_devices"`
	} `json:"subscriptions"`
}

func (b *burster) trigger(ctx context.Context, seq int, userID string, group *burstGroup) (*burstTrigger, error) {
	uid := url.PathEscape(userID)
	resp, _ := b.call(ctx, request{method: http.MethodGet, path: "/v1/users/" + uid, tmpl: "/v1/users/{id}"})
	if resp.status != http.StatusOK {
		return nil, fmt.Errorf("burst: reading the user failed: %s", describe(resp))
	}
	var d userDetail
	if err := json.Unmarshal(resp.body, &d); err != nil {
		return nil, fmt.Errorf("burst: decode user detail: %w", err)
	}
	tr := &burstTrigger{Seq: seq, OK: true, Push: "none: nodes see the change on their next poll"}

	if b.cfg.op != opGroup {
		w, err := b.toggleDeviceLimit(ctx, d)
		if err != nil {
			return nil, err
		}
		tr.Writes = append(tr.Writes, w)
		tr.TriggerAt, tr.DoneAt = w.SentAt, w.DoneAt
	}
	if b.cfg.op != opDeviceLimit {
		w, err := b.toggleGroup(ctx, uid, d, group)
		if err != nil {
			return nil, err
		}
		tr.Writes = append(tr.Writes, w)
		tr.TriggerAt, tr.DoneAt = w.SentAt, w.DoneAt
		tr.Push = "tenant node.users.changed: every connected node re-lists its users (full sync_users when the set changed)"
	}
	tr.TriggerUnix = tr.TriggerAt.UnixMilli()
	var failed error
	for _, w := range tr.Writes {
		if w.Status != http.StatusOK {
			tr.OK = false
			failed = fmt.Errorf("burst: %s returned HTTP %d %s", w.Endpoint, w.Status, w.ErrorCode)
		}
	}
	return tr, failed
}

func (b *burster) toggleDeviceLimit(ctx context.Context, d userDetail) (burstWrite, error) {
	for _, s := range d.Subscriptions {
		if s.Status != "active" && s.Status != "trialing" && s.Status != "grace" {
			continue
		}
		next := nextDeviceLimit(s.DeviceLimitOverride, s.PlanMaxDevices)
		w := burstWrite{
			Endpoint: endpointName("admin", http.MethodPost, "/v1/subscriptions/{id}/device-limit"),
			Field:    "subscriptions.device_limit", SubscriptionID: s.ID,
			From: s.DeviceLimitOverride, To: next,
			EffectiveFrom: ptr(effectiveLimit(s.DeviceLimitOverride, s.PlanMaxDevices)),
			EffectiveTo:   ptr(effectiveLimit(next, s.PlanMaxDevices)),
		}
		b.send(ctx, &w, request{method: http.MethodPost,
			path: "/v1/subscriptions/" + url.PathEscape(s.ID) + "/device-limit", tmpl: "/v1/subscriptions/{id}/device-limit",
			body: map[string]*int{"limit": next}})
		return w, nil
	}
	return burstWrite{}, errors.New("burst: the user has no active subscription to change; pick another -user-email or use -op group")
}

func (b *burster) toggleGroup(ctx context.Context, uid string, d userDetail, group *burstGroup) (burstWrite, error) {
	var from any
	to := group.ID
	switch {
	case d.GroupID == nil || *d.GroupID == "":
	case *d.GroupID == group.ID:
		from, to = group.ID, ""
	default:
		return burstWrite{}, errors.New("burst: the user is already in another group; toggling would lose it, pick another -user-email")
	}
	w := burstWrite{
		Endpoint: endpointName("admin", http.MethodPost, "/v1/users/{id}/group"),
		Field:    "users.user_group_id", From: from, To: to,
	}
	if to == "" {
		w.To = nil
	}
	b.send(ctx, &w, request{method: http.MethodPost, path: "/v1/users/" + uid + "/group", tmpl: "/v1/users/{id}/group",
		body: map[string]string{"group_id": to}})
	return w, nil
}

func (b *burster) send(ctx context.Context, w *burstWrite, rq request) {
	w.SentAt = time.Now()
	resp, reauthed := b.call(ctx, rq)
	w.DoneAt = time.Now()
	w.LatencyMS = float64(w.DoneAt.Sub(w.SentAt).Microseconds()) / 1000
	w.Status, w.ErrorCode, w.Reauthed = resp.status, resp.code, reauthed
}

// nextDeviceLimit 来回切换订阅的设备数覆盖：有覆盖就恢复套餐规定（null），没有就设成一个
// 与当前生效值不同的数，保证节点用户表里这个人的 device_limit 真的变了。
func nextDeviceLimit(override, planMax *int) *int {
	if override != nil {
		return nil
	}
	eff := effectiveLimit(nil, planMax)
	switch {
	case eff == 0: // 0 是不限，换成一个大数：节点上的值变了，对这个虚构用户没有实际限制
		return ptr(1000)
	case eff >= 1000:
		return ptr(999)
	}
	return ptr(eff + 1)
}

// effectiveLimit 与 ListNodeUsers 同口径：COALESCE(订阅覆盖, 套餐 max_devices, 0)。
func effectiveLimit(override, planMax *int) int {
	switch {
	case override != nil:
		return *override
	case planMax != nil:
		return *planMax
	}
	return 0
}

func ptr(v int) *int { return &v }

func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
