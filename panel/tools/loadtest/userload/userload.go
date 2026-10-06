// Package userload 模拟用户侧的混合流量（订阅拉取、门户页面、后台列表）与 burst（改一个用户让全部节点重拉用户表）。
//
// 每个模拟用户用 manifest 里固定的来源 IP，经 X-Real-IP 带给面板：压测机经 nginx 打面板时
// 所有请求同一个来源地址，会把风控的 IP 聚类打满、让按 IP 的限流与风控查询的耗时失真。
// 测试专用的 nginx 片段只对压测机信任这个头（realip 模块），生产配置不受影响。
package userload

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// 管理员凭据只经 flag 或这两个环境变量传入，仓库里不出现任何真实值。
const (
	envAdminEmail    = "LOADTEST_ADMIN_EMAIL"
	envAdminPassword = "LOADTEST_ADMIN_PASSWORD"
)

// defaultAdminIP 是后台流量的固定来源地址（RFC 5737 文档网段，虚构）。
const defaultAdminIP = "198.51.100.200"

// defaultIPHeader 是承载模拟来源 IP 的请求头；经带 Cloudflare realip 的 nginx 时加上 CF-Connecting-IP（见 client.go）。
const (
	defaultIPHeader = "X-Real-IP"
	ipHeadersUsage  = "comma-separated headers carrying the simulated source IP; add CF-Connecting-IP when going through the stock nginx with the load machine in set_real_ip_from"
)

type usersConfig struct {
	manifest        string
	steadyStart     int64
	steadyDur       time.Duration
	publicURL       string
	adminURL        string
	admin           credentials
	adminIPs        []string
	subRate         float64
	subInterval     time.Duration // >0 时按「每人多久拉一次」折算 subRate，与档位无关
	portalRate      float64
	adminRate       float64
	loginRate       float64
	warmupLoginRate float64
	portalUsers     int
	subPrefix       string
	duration        time.Duration
	timeout         time.Duration
	window          time.Duration
	progressEvery   time.Duration
	maxInflight     int
	out             string
	strict          bool
	ipHeaders       []string
	limits          panelLimits
}

// Main 是 users 子命令：go run ./tools/loadtest users -manifest ... -public-url ... [-admin-url ...]
func Main(args []string) error {
	cfg, err := parseUsersFlags(args)
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
	_, err = runUsers(ctx, cfg, m, os.Stdout)
	return err
}

func parseUsersFlags(args []string) (usersConfig, error) {
	var cfg usersConfig
	fs := flag.NewFlagSet("users", flag.ContinueOnError)
	fs.StringVar(&cfg.manifest, "manifest", "", "manifest written by seed (required)")
	fs.Int64Var(&cfg.steadyStart, "steady-start", 0, "steady window start (unix seconds); with -steady-dur the report adds per-endpoint stats inside the window")
	fs.DurationVar(&cfg.steadyDur, "steady-dur", 0, "steady window length")
	fs.StringVar(&cfg.publicURL, "public-url", "", "public gateway base URL (portal API and subscription links)")
	fs.StringVar(&cfg.adminURL, "admin-url", "", "admin gateway base URL including any secret path prefix (required when -admin-rate > 0)")
	fs.StringVar(&cfg.admin.email, "admin-email", "", "admin email (default $"+envAdminEmail+")")
	fs.StringVar(&cfg.admin.password, "admin-password", "", "admin password (prefer $"+envAdminPassword+": flags show up in ps)")
	adminIPs := fs.String("admin-ips", defaultAdminIP, "comma-separated fictitious source IPs for admin traffic, one admin session per IP")
	ipHeaders := fs.String("ip-headers", defaultIPHeader, ipHeadersUsage)
	fs.Float64Var(&cfg.subRate, "sub-rate", 2, "subscription pulls per second, round-robin over all manifest users")
	fs.DurationVar(&cfg.subInterval, "sub-interval", 0, "per-user subscription pull interval (e.g. 30m to test headroom, 6h close to real clients); overrides -sub-rate with users/interval")
	fs.Float64Var(&cfg.portalRate, "portal-rate", 2, "portal page reads per second, random user from the active pool")
	fs.Float64Var(&cfg.adminRate, "admin-rate", 0.5, "admin list reads per second")
	fs.Float64Var(&cfg.loginRate, "login-rate", 0.05, "portal re-logins per second during the run (Argon2 per login)")
	fs.Float64Var(&cfg.warmupLoginRate, "warmup-login-rate", 10, "logins per second while logging in the active pool before the run")
	fs.IntVar(&cfg.portalUsers, "portal-users", 200, "active portal pool: N manifest users spread evenly over the list log in once and reuse the token")
	fs.StringVar(&cfg.subPrefix, "sub-prefix", "", "tenant subscription path prefix (default: manifest subscribe_path_prefix, else read from a pool user's subscription link)")
	fs.DurationVar(&cfg.duration, "duration", time.Minute, "measured run length (warm-up not included)")
	fs.DurationVar(&cfg.timeout, "timeout", 30*time.Second, "per-request timeout")
	fs.DurationVar(&cfg.window, "window", 10*time.Second, "timeline window in the report")
	fs.DurationVar(&cfg.progressEvery, "progress", time.Minute, "progress line interval")
	fs.IntVar(&cfg.maxInflight, "max-inflight", 512, "in-flight request cap; ticks beyond it are dropped and counted")
	fs.StringVar(&cfg.out, "out", "loadtest-out", "directory for users.json / users.txt / users-warmup.*")
	fs.BoolVar(&cfg.strict, "strict", false, "exit non-zero on any 5xx, transport error or dropped tick")
	fs.IntVar(&cfg.limits.ipPerMin, "rl-ip-per-min", 120, "panel AEGIS_RL_IP_PER_MIN, for the preflight estimate")
	fs.IntVar(&cfg.limits.accountPerMin, "rl-account-per-min", 300, "panel AEGIS_RL_ACCOUNT_PER_MIN, for the preflight estimate")
	fs.IntVar(&cfg.limits.authPerMin, "rl-auth-per-min", 10, "panel AEGIS_RL_AUTH_PER_MIN, for the preflight estimate")
	fs.IntVar(&cfg.limits.subPerHour, "rl-sub-per-hour", 60, "subscription credential rate_limit_per_hour, for the preflight estimate")
	fs.IntVar(&cfg.limits.adminIPPerMin, "rl-admin-ip-per-min", 240, "admin gateway per-IP limit, for the preflight estimate")
	if err := fs.Parse(args); err != nil {
		return cfg, err
	}
	if cfg.admin.email == "" {
		cfg.admin.email = os.Getenv(envAdminEmail)
	}
	if cfg.admin.password == "" {
		cfg.admin.password = os.Getenv(envAdminPassword)
	}
	cfg.adminIPs = splitList(*adminIPs)
	cfg.ipHeaders = splitList(*ipHeaders)
	return cfg, cfg.validate()
}

// signalContext 在 SIGINT/SIGTERM 时取消：第一下收尾并写结果，之后恢复默认处理，再按一下直接退出。
func signalContext() (context.Context, context.CancelFunc) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go func() {
		<-ctx.Done()
		stop()
	}()
	return ctx, stop
}

func splitList(s string) []string {
	var out []string
	for _, v := range strings.Split(s, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func (cfg usersConfig) validate() error {
	switch {
	case cfg.manifest == "":
		return errors.New("users: -manifest is required")
	case cfg.subRate < 0 || cfg.portalRate < 0 || cfg.adminRate < 0 || cfg.loginRate < 0:
		return errors.New("users: rates must be >= 0")
	case cfg.subInterval < 0:
		return errors.New("users: -sub-interval must be >= 0")
	case cfg.publicURL == "" && cfg.subRate+cfg.portalRate+cfg.loginRate > 0:
		return errors.New("users: -public-url is required for subscription and portal traffic")
	case cfg.adminRate > 0 && cfg.adminURL == "":
		return errors.New("users: -admin-url is required when -admin-rate > 0")
	case cfg.adminRate > 0 && (cfg.admin.email == "" || cfg.admin.password == ""):
		return fmt.Errorf("users: admin traffic needs -admin-email/-admin-password or $%s/$%s", envAdminEmail, envAdminPassword)
	case cfg.adminRate > 0 && len(cfg.adminIPs) == 0:
		return errors.New("users: -admin-ips is empty")
	case cfg.maxInflight <= 0 || cfg.duration <= 0 || cfg.timeout <= 0:
		return errors.New("users: -max-inflight, -duration and -timeout must be positive")
	case len(cfg.ipHeaders) == 0:
		return errors.New("users: -ip-headers is empty")
	case cfg.warmupLoginRate <= 0:
		return errors.New("users: -warmup-login-rate must be positive")
	}
	return nil
}

// ---------------------------------------------------------------------------
// 运行
// ---------------------------------------------------------------------------

// runUsers 是 users 的全过程，ctx 取消（SIGINT/SIGTERM）时提前收尾、照样写结果。
func runUsers(ctx context.Context, cfg usersConfig, m *ltkit.Manifest, stdout io.Writer) (ltkit.Report, error) {
	users, err := actorsOf(m)
	if err != nil {
		return ltkit.Report{}, err
	}
	poolN := min(cfg.portalUsers, len(users))
	if cfg.portalRate+cfg.loginRate > 0 && poolN <= 0 {
		return ltkit.Report{}, errors.New("users: portal traffic needs -portal-users > 0")
	}
	pool := spreadPool(users, poolN)

	// 按每人间隔给速率：同一个 -sub-interval 在 5k / 10k / 15k 三档自动折成各自的总速率
	if cfg.subInterval > 0 {
		cfg.subRate = float64(len(users)) / cfg.subInterval.Seconds()
		fmt.Fprintf(stdout, "[users] -sub-interval %s over %d users = %.2f subscription pulls/s\n", cfg.subInterval, len(users), cfg.subRate)
	}

	lines, warns := preflight(cfg, users, pool)
	fmt.Fprintf(stdout, "[users] manifest %q: %d users, active pool %d; rate-limit preflight:\n", m.Label, len(users), len(pool))
	for _, l := range lines {
		fmt.Fprintln(stdout, "  "+l)
	}

	// seed 写进清单的订阅前缀与每人的订阅 id：前缀优先级 -sub-prefix > manifest > 门户探测，
	// 订阅 id 有就不在预热里再查
	prefix := cfg.subPrefix
	if prefix == "" {
		prefix = m.SubscribePathPrefix
	}
	subIDs := make(map[string]string, len(m.Users))
	for _, u := range m.Users {
		if u.SubscriptionID != "" {
			subIDs[u.ID] = u.SubscriptionID
		}
	}
	for _, a := range users {
		if id, ok := subIDs[a.u.ID]; ok {
			a.subID.Store(&id)
		}
	}

	c := newClient(cfg.maxInflight, cfg.timeout, cfg.ipHeaders)
	t := &traffic{
		c: c, pub: newGateway("public", cfg.publicURL), adm: newGateway("admin", cfg.adminURL),
		prefix: prefix, users: users, pool: pool, pass: m.UserPassword,
		portal: newPicker(portalReads), admin: newPicker(adminReads),
	}

	warm := ltkit.NewRecorder("users-warmup", cfg.window)
	werr := warmup(ctx, cfg, t, warm, stdout)
	if _, err := warm.WriteFiles(cfg.out); err != nil {
		return ltkit.Report{}, err
	}
	if werr != nil {
		return ltkit.Report{}, werr
	}

	rec := ltkit.NewRecorder("users", cfg.window)
	if cfg.steadyStart > 0 && cfg.steadyDur > 0 {
		rec.SetSteady(time.Unix(cfg.steadyStart, 0), cfg.steadyDur)
	}
	t.rec = rec
	classes := []*class{
		{name: "sub", rate: cfg.subRate, fire: func(ctx context.Context) bool { t.pullSubscription(ctx); return true }},
		{name: "portal", rate: cfg.portalRate, fire: t.portalRead},
		{name: "admin", rate: cfg.adminRate, fire: func(ctx context.Context) bool { t.adminRead(ctx); return true }},
		{name: "login", rate: cfg.loginRate, fire: func(ctx context.Context) bool { t.relogin(ctx); return true }},
	}
	setUsersMeta(rec, cfg, m, len(users), len(pool), warns)

	runCtx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()
	lim := newLimiter(cfg.maxInflight)
	// 在途请求不随调度停止而取消：让它们按 -timeout 自然收尾、照常计量
	reqCtx := context.WithoutCancel(ctx)
	done := make(chan struct{})
	go func() {
		progress(runCtx, stdout, cfg.progressEvery, rec, classes, lim)
		close(done)
	}()
	fmt.Fprintf(stdout, "[users] running %s: sub %.2f/s portal %.2f/s admin %.2f/s login %.2f/s (open loop, max in-flight %d)\n",
		cfg.duration, cfg.subRate, cfg.portalRate, cfg.adminRate, cfg.loginRate, cfg.maxInflight)
	finished := make(chan struct{})
	for _, cl := range classes {
		go func() {
			cl.run(runCtx, reqCtx, lim)
			finished <- struct{}{}
		}()
	}
	for range classes {
		<-finished
	}
	<-done
	rec.Stop()
	interrupted := ctx.Err() != nil
	if !lim.drain(cfg.timeout + 5*time.Second) {
		fmt.Fprintf(stdout, "[users] %d requests still in flight after the drain grace; writing results anyway\n", lim.inflight())
	}

	for _, cl := range classes {
		rec.SetMeta("sent_"+cl.name, cl.sent.Load())
		rec.SetMeta("dropped_"+cl.name, cl.dropped.Load())
		rec.SetMeta("skipped_"+cl.name, cl.skipped.Load())
	}
	rec.SetMeta("interrupted", interrupted)
	rep, err := rec.WriteFiles(cfg.out)
	if err != nil {
		return rep, err
	}
	ltkit.WriteSummary(stdout, rep)
	fmt.Fprintf(stdout, "[users] results in %s/users.json and %s/users.txt\n", cfg.out, cfg.out)
	if cfg.strict {
		return rep, strictCheck(rep, classes)
	}
	return rep, nil
}

// spreadPool 按等间距从全体用户里挑活跃池，而不是取前 n 个：seed 多半按序分配来源地址，
// 前 n 个会挤在同一两个 /24 里，把门户流量与预热登录全压到 pub_net / auth_net 的同一个桶上。
func spreadPool(users []*actor, n int) []*actor {
	if n <= 0 {
		return nil
	}
	pool := make([]*actor, 0, n)
	for i := range n {
		pool = append(pool, users[i*len(users)/n])
	}
	return pool
}

func actorsOf(m *ltkit.Manifest) ([]*actor, error) {
	if len(m.Users) == 0 {
		return nil, errors.New("users: manifest has no users")
	}
	if m.UserPassword == "" {
		return nil, errors.New("users: manifest has no user_password")
	}
	out := make([]*actor, 0, len(m.Users))
	for i, u := range m.Users {
		if u.RealIP == "" || u.Email == "" || u.SubscribeToken == "" {
			return nil, fmt.Errorf("users: manifest user #%d lacks email, subscribe_token or real_ip", i)
		}
		out = append(out, &actor{u: u, ua: uaForUser(i)})
	}
	return out, nil
}

func setUsersMeta(rec *ltkit.Recorder, cfg usersConfig, m *ltkit.Manifest, users, pool int, warns []string) {
	rec.SetMeta("label", m.Label)
	rec.SetMeta("users", users)
	rec.SetMeta("portal_pool", pool)
	rec.SetMeta("admin_ips", len(cfg.adminIPs))
	rec.SetMeta("rate_sub", cfg.subRate)
	rec.SetMeta("sub_interval_s", cfg.subInterval.Seconds())
	rec.SetMeta("rate_portal", cfg.portalRate)
	rec.SetMeta("rate_admin", cfg.adminRate)
	rec.SetMeta("rate_login", cfg.loginRate)
	rec.SetMeta("duration_target_s", cfg.duration.Seconds())
	rec.SetMeta("max_inflight", cfg.maxInflight)
	rec.SetMeta("preflight_warnings", warns)
	mix := map[string]int{}
	for i := range users {
		mix[uaForUser(i).client]++
	}
	rec.SetMeta("ua_mix", mix)
}

// strictCheck：任一 5xx、无响应或丢拍都判失败（丢拍意味着这一档的结果不可信）。
func strictCheck(rep ltkit.Report, classes []*class) error {
	t := tallyCodes(rep.Totals.Codes)
	var dropped int64
	for _, cl := range classes {
		dropped += cl.dropped.Load()
	}
	if t.c5xx > 0 || t.transport > 0 || dropped > 0 {
		return fmt.Errorf("strict: %d responses 5xx, %d transport errors, %d dropped ticks", t.c5xx, t.transport, dropped)
	}
	return nil
}
