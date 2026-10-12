package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	audith "github.com/aegispanel/aegis/internal/module/audit/handler"
	authh "github.com/aegispanel/aegis/internal/module/auth/handler"
	catalogh "github.com/aegispanel/aegis/internal/module/catalog/handler"
	commissionh "github.com/aegispanel/aegis/internal/module/commission/handler"
	idemh "github.com/aegispanel/aegis/internal/module/idempotency/handler"
	identityh "github.com/aegispanel/aegis/internal/module/identity/handler"
	ledgerh "github.com/aegispanel/aegis/internal/module/ledger/handler"
	notifyh "github.com/aegispanel/aegis/internal/module/notify/handler"
	orderh "github.com/aegispanel/aegis/internal/module/order/handler"
	paymenth "github.com/aegispanel/aegis/internal/module/payment/handler"
	promoh "github.com/aegispanel/aegis/internal/module/promo/handler"
	settingsh "github.com/aegispanel/aegis/internal/module/settings/handler"
	subh "github.com/aegispanel/aegis/internal/module/subscription/handler"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/aegispanel/aegis/internal/pkg/route"
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

	publicEng, adminEng := newEngines(r, svc)

	publicSrv := newHTTPServer(r.cfg.HTTP.Public, publicEng)
	adminSrv := newHTTPServer(r.cfg.HTTP.Admin, adminEng)

	var pprofSrvs []*http.Server
	if addr := r.cfg.HTTP.Public.PprofAddr; addr != "" {
		pprofSrvs = append(pprofSrvs, startPprofServer(addr))
	}
	if addr := r.cfg.HTTP.Admin.PprofAddr; addr != "" && addr != r.cfg.HTTP.Public.PprofAddr {
		pprofSrvs = append(pprofSrvs, startPprofServer(addr))
	}

	r.log.Info("serve started",
		zap.String("public", r.cfg.HTTP.Public.Addr),
		zap.String("admin", r.cfg.HTTP.Admin.Addr),
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
	for _, ps := range pprofSrvs {
		pprofSrv := ps
		g.Go(func() error {
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				return fmt.Errorf("pprof 监听 (%s): %w", pprofSrv.Addr, err)
			}
			return nil
		})
	}

	g.Go(func() error {
		<-gctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := publicSrv.Shutdown(shutCtx); err != nil {
			r.log.Warn("关闭门户 HTTP", zap.Error(err))
		}
		if err := adminSrv.Shutdown(shutCtx); err != nil {
			r.log.Warn("关闭管理 HTTP", zap.Error(err))
		}
		for _, ps := range pprofSrvs {
			if err := ps.Shutdown(shutCtx); err != nil {
				r.log.Warn("关闭 pprof", zap.String("addr", ps.Addr), zap.Error(err))
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
	}
}

// newEngines 建门户与后台两个引擎并挂上各模块路由。
// 引擎级顺序：基础中间件（请求号、日志、恢复、错误、安全头、并发、超时）→（后台）只读门 → 鉴权 → 域守卫；
// 限流、登录要求、重认证、幂等、开关是路由级，由各模块从 Kit 取；后台不分权限，登录了就是管理员。
func newEngines(r *runtime, svc *services) (*gin.Engine, *gin.Engine) {
	settingsH := settingsh.New(svc.settings)
	authH := authh.New(svc.auth, svc.limiter, authOptions(r.cfg))
	idemH := idemh.New(svc.idem)
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
		notifyh.New(svc.notify),
		ledgerh.New(svc.ledger),
		catalogh.New(svc.catalog),
		promoh.New(svc.promo),
		subh.New(svc.sub, svc.limiter),
		paymenth.New(svc.payment, svc.limiter),
		orderh.New(svc.order, svc.limiter),
		commissionh.New(svc.commission),
	}

	publicEng := newEngine(r, r.cfg.HTTP.Public,
		authH.Authenticate(authn.AudiencePublic),
		authH.DomainGuard(authn.AudiencePublic),
	)
	adminEng := newEngine(r, r.cfg.HTTP.Admin,
		settingsH.AdminWritesGate(),
		authH.Authenticate(authn.AudienceAdmin),
		authH.DomainGuard(authn.AudienceAdmin),
	)
	publicV1 := publicEng.Group("/v1")
	adminV1 := adminEng.Group("/v1")
	for _, m := range mods {
		m.RegisterPublic(publicV1, kit)
		m.RegisterAdmin(adminV1, kit)
	}
	return publicEng, adminEng
}

// authOptions 是 auth handler 的选项：refresh cookie 只在生产带 Secure（本机开发走 http）。
func authOptions(cfg *config.Config) authh.Options {
	return authh.Options{AuthPerMin: cfg.RateLimit.AuthPerMin, SecureCookie: cfg.IsProduction()}
}

func newEngine(r *runtime, sc config.HTTPServer, extra ...gin.HandlerFunc) *gin.Engine {
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
		httpx.Recovery(),
		httpx.Errors(),
		httpx.SecurityHeaders(),
		httpx.Concurrency(sc.MaxConcurrency),
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

// isStream 认出 SSE 端点（public、admin 都是 …/events），它们不受请求超时限制。
func isStream(c *gin.Context) bool {
	return strings.HasSuffix(c.Request.URL.Path, "/events")
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
