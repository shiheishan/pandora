package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"sync"
	"time"

	appearanceh "github.com/aegispanel/aegis/internal/module/appearance/handler"
	audith "github.com/aegispanel/aegis/internal/module/audit/handler"
	authh "github.com/aegispanel/aegis/internal/module/auth/handler"
	catalogh "github.com/aegispanel/aegis/internal/module/catalog/handler"
	certsh "github.com/aegispanel/aegis/internal/module/certs/handler"
	commissionh "github.com/aegispanel/aegis/internal/module/commission/handler"
	contenth "github.com/aegispanel/aegis/internal/module/content/handler"
	giftcardh "github.com/aegispanel/aegis/internal/module/giftcard/handler"
	idemh "github.com/aegispanel/aegis/internal/module/idempotency/handler"
	identityh "github.com/aegispanel/aegis/internal/module/identity/handler"
	ledgerh "github.com/aegispanel/aegis/internal/module/ledger/handler"
	nodeh "github.com/aegispanel/aegis/internal/module/node/handler"
	notifyh "github.com/aegispanel/aegis/internal/module/notify/handler"
	orderh "github.com/aegispanel/aegis/internal/module/order/handler"
	paymenth "github.com/aegispanel/aegis/internal/module/payment/handler"
	pluginh "github.com/aegispanel/aegis/internal/module/plugin/handler"
	promoh "github.com/aegispanel/aegis/internal/module/promo/handler"
	realtimeh "github.com/aegispanel/aegis/internal/module/realtime/handler"
	reporth "github.com/aegispanel/aegis/internal/module/report/handler"
	settingsh "github.com/aegispanel/aegis/internal/module/settings/handler"
	subh "github.com/aegispanel/aegis/internal/module/subscription/handler"
	supporth "github.com/aegispanel/aegis/internal/module/support/handler"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/metrics"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/aegispanel/aegis/internal/pkg/route"
	"github.com/aegispanel/aegis/internal/pkg/webapp"
	"github.com/aegispanel/aegis/web"
	"github.com/gin-gonic/gin"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
)

var ginReleaseOnce sync.Once

func newServeCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "启动门户与管理 HTTP 服务",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runServe(cmd.Context(), *configPath)
		},
	}
}

func runServe(ctx context.Context, configPath string) error {
	r, err := openRuntime(ctx, configPath)
	if err != nil {
		return err
	}
	defer r.close()

	svc, err := buildServices(r)
	if err != nil {
		return err
	}
	defer svc.close()

	reg, err := newMetricsRegistry(r)
	if err != nil {
		return err
	}
	httpm := metrics.NewHTTP(reg)

	nodeCh, err := setupNodeChannel(ctx, r, svc, reg)
	if err != nil {
		return err
	}

	// 单实例 SSE 连接总上限，超出回 503。
	const maxSSEStreams = 10000
	hub := realtime.NewHub(r.redis, r.log, maxSSEStreams)
	svc.report.UseStreams(hub)

	publicEng, adminEng := newEngines(r, svc, hub, httpm)

	publicSrv := newHTTPServer(r.cfg.HTTP.Public, publicEng)
	adminSrv := newHTTPServer(r.cfg.HTTP.Admin, adminEng)

	var pprofSrvs []*http.Server
	if addr := r.cfg.HTTP.Public.PprofAddr; addr != "" {
		pprofSrvs = append(pprofSrvs, startPprofServer(addr))
	}
	if addr := r.cfg.HTTP.Admin.PprofAddr; addr != "" && addr != r.cfg.HTTP.Public.PprofAddr {
		pprofSrvs = append(pprofSrvs, startPprofServer(addr))
	}

	var metricsSrv *http.Server
	if addr := r.cfg.Observability.MetricsAddr; addr != "" {
		metricsSrv = metrics.NewServer(addr, reg)
	}

	r.log.Info("serve started",
		zap.String("public", r.cfg.HTTP.Public.Addr),
		zap.String("admin", r.cfg.HTTP.Admin.Addr),
		zap.String("node_grpc", r.cfg.GRPC.Node.Addr),
		zap.String("metrics", r.cfg.Observability.MetricsAddr),
	)

	g, gctx := errgroup.WithContext(ctx)

	// 开关缓存按纪元失效：监听变更，随 gctx 结束（监听端口失败也会收尾）
	g.Go(func() error {
		svc.settings.WatchSwitches(gctx)
		return nil
	})

	g.Go(func() error {
		if err := publicSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("门户 HTTP 监听: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		if err := adminSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("管理 HTTP 监听: %w", err)
		}
		return nil
	})
	nodeCh.run(gctx, g.Go)
	for _, ps := range pprofSrvs {
		pprofSrv := ps
		g.Go(func() error {
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("pprof 监听 (%s): %w", pprofSrv.Addr, err)
			}
			return nil
		})
	}
	if metricsSrv != nil {
		g.Go(func() error {
			if err := metricsSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("指标监听 (%s): %w", metricsSrv.Addr, err)
			}
			return nil
		})
	}

	g.Go(func() error {
		<-gctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		// 先让 SSE 长连接全部结束，否则 HTTP Shutdown 会一直等它们到超时
		hub.Close()
		// 节点会话错峰重连要约 12 秒发完，给它 15 秒，剩下的留给 HTTP
		nodeCtx, nodeCancel := context.WithTimeout(shutCtx, 15*time.Second)
		nodeCh.shutdown(nodeCtx, r.log)
		nodeCancel()
		// 两个监听并发排空：一个拖到超时，另一个的在途请求照样等得到
		var wg sync.WaitGroup
		for name, srv := range map[string]*http.Server{"门户": publicSrv, "管理": adminSrv} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := srv.Shutdown(shutCtx); err != nil {
					r.log.Warn("关闭 HTTP", zap.String("server", name), zap.Error(err))
				}
			}()
		}
		wg.Wait()
		for _, ps := range pprofSrvs {
			if err := ps.Shutdown(shutCtx); err != nil {
				r.log.Warn("关闭 pprof", zap.String("addr", ps.Addr), zap.Error(err))
			}
		}
		if metricsSrv != nil {
			if err := metricsSrv.Shutdown(shutCtx); err != nil {
				r.log.Warn("关闭指标服务", zap.Error(err))
			}
		}
		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}
	return nil
}

func newHTTPServer(sc config.HTTPServer, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              sc.Addr,
		Handler:           h,
		ReadHeaderTimeout: sc.ReadHeaderTimeout,
		ReadTimeout:       sc.ReadTimeout,
		WriteTimeout:      sc.WriteTimeout,
		IdleTimeout:       sc.IdleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// maxHeaderBytes 是请求头总量上限（Go 缺省 1 MiB）。前面的 nginx 单行头最多 8 KiB、共 4 块，
// 正常请求（含 refresh cookie 与 Authorization）远小于它；这里收紧是防绕过 nginx 直连回环端口时的大头。
const maxHeaderBytes = 64 << 10

// newEngines 建门户与后台两个引擎并挂上各模块路由。
// 引擎级顺序：基础中间件（请求号、日志、指标、恢复、错误、安全头、并发、超时）→（后台）只读门 → 鉴权 → 域守卫；
// 限流、登录要求、重认证、幂等、开关是路由级，由各模块从 Kit 取；后台不分权限，登录了就是管理员。
func newEngines(r *runtime, svc *services, hub *realtime.Hub, httpm *metrics.HTTP) (*gin.Engine, *gin.Engine) {
	settingsH := settingsh.New(svc.settings)
	authH := authh.New(svc.auth, svc.limiter, authOptions(r.cfg))
	idemH := idemh.New(svc.idem, svc.idemFPKey)
	// 套餐列表的可服务节点数由 node 数（catalog 不依赖 node）
	catalogH := catalogh.New(svc.catalog)
	catalogH.UseNodeCounter(svc.node)
	notifyH := notifyh.New(svc.notify)
	notifyH.UseLimiter(svc.limiter)
	kit := route.Kit{
		RequireAuth:  authH.RequireAuth(),
		RecentReauth: authH.RecentReauth(),
		Idempotency:  idemH.Middleware,
		Switch:       settingsH.Switch,
	}
	mods := []route.Module{
		settingsH,
		audith.New(svc.audit),
		identityh.New(svc.identity, svc.limiter, r.cfg.RateLimit.AuthPerMin),
		authH,
		notifyH,
		ledgerh.New(svc.ledger),
		catalogH,
		promoh.New(svc.promo),
		subh.New(svc.sub, svc.limiter, r.cfg.RateLimit.IPPerMin),
		nodeh.New(svc.node),
		paymenth.New(svc.payment, svc.limiter),
		orderh.New(svc.order, svc.limiter),
		giftcardh.New(svc.giftcard, svc.limiter),
		commissionh.New(svc.commission),
		contenth.New(svc.content, svc.limiter, r.cfg.RateLimit.AccountPerMin),
		appearanceh.New(svc.appearance),
		supporth.New(svc.support, svc.limiter),
		certsh.New(svc.certs),
		pluginh.New(svc.plugin),
		reporth.New(svc.report),
		realtimeh.New(hub, r.cfg.Auth.AccessTTL),
	}

	publicEng := newEngine(r, r.cfg.HTTP.Public, httpm.Middleware("public"),
		authH.Authenticate(authn.AudiencePublic),
		authH.DomainGuard(authn.AudiencePublic),
	)
	adminEng := newEngine(r, r.cfg.HTTP.Admin, httpm.Middleware("admin"),
		settingsH.AdminWritesGate(),
		authH.Authenticate(authn.AudienceAdmin),
		authH.DomainGuard(authn.AudienceAdmin),
	)
	// 前端页面：门户与后台各自的构建产物挂在各自引擎的 / 与 /assets/*；
	// 门户开了人机验证时入口页 CSP 按设置追加那一家的来源（后台不用人机验证，CSP 不变）；
	// 两个入口页的 <title>、描述与图标按站点名（系统设置）和生效主题的标语、Logo 改写（appearance 的进程内快照，不每次查库）
	webapp.Mount(publicEng, web.PortalApp, webapp.WithExtraSources(portalCaptchaCSP(svc.settings)),
		webapp.WithIndexMeta(appearanceh.IndexMeta(svc.appearance, appearanceh.PagePortal)))
	webapp.Mount(adminEng, web.AdminApp, webapp.WithIndexMeta(appearanceh.IndexMeta(svc.appearance, appearanceh.PageAdmin)))
	publicV1 := publicEng.Group("/v1")
	adminV1 := adminEng.Group("/v1")
	for _, m := range mods {
		m.RegisterPublic(publicV1, kit)
		m.RegisterAdmin(adminV1, kit)
		if rm, ok := m.(route.RootModule); ok {
			rm.RegisterRoot(publicEng)
		}
	}
	return publicEng, adminEng
}

// authOptions 是 auth handler 的选项：refresh cookie 只在生产带 Secure（本机开发走 http）。
func authOptions(cfg *config.Config) authh.Options {
	return authh.Options{AuthPerMin: cfg.RateLimit.AuthPerMin, SecureCookie: cfg.IsProduction()}
}

func newEngine(r *runtime, sc config.HTTPServer, metricsMW gin.HandlerFunc, extra ...gin.HandlerFunc) *gin.Engine {
	if r.cfg.Env != config.EnvDevelopment {
		ginReleaseOnce.Do(func() { gin.SetMode(gin.ReleaseMode) })
	}

	e := gin.New()
	// 两个监听只绑回环，前面是 nginx：只信 nginx 覆写的 X-Real-IP，不解析 X-Forwarded-For 链。
	e.TrustedPlatform = "X-Real-IP"
	if err := e.SetTrustedProxies(nil); err != nil {
		panic(err) // nil 列表不会出错
	}

	e.Use(
		httpx.RequestID(r.log),
		httpx.AccessLog(),
		metricsMW,
		httpx.Recovery(),
		httpx.Errors(),
		httpx.SecurityHeaders(r.cfg.IsProduction()),
		httpx.Concurrency(sc.MaxConcurrency, isStream),
		httpx.Timeout(sc.RequestTimeout, isStream),
	)
	e.Use(extra...)

	e.GET("/healthz", func(c *gin.Context) {
		httpx.OK(c, gin.H{"status": "ok"})
	})
	e.GET("/readyz", func(c *gin.Context) {
		readyCtx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
		defer cancel()
		if err := r.db.Ping(readyCtx); err != nil {
			httpx.Fail(c, errs.Wrap(errs.Unavailable, err))
			return
		}
		if err := redisx.Ping(readyCtx, r.redis); err != nil {
			httpx.Fail(c, errs.Wrap(errs.Unavailable, err))
			return
		}
		httpx.OK(c, gin.H{"status": "ok"})
	})
	e.NoRoute(httpx.NoRoute)

	return e
}

// isStream 认出 SSE 端点（public、admin 都是 /v1/events），用于请求超时与并发名额豁免。
// 按匹配到的路由模板判，未注册的路径（FullPath 为空）不会被豁免。
func isStream(c *gin.Context) bool {
	return c.FullPath() == "/v1/events"
}

// startPprofServer 在独立回环地址上暴露 net/http/pprof，不使用 DefaultServeMux。
func startPprofServer(addr string) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
}
