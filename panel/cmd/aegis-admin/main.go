// Command aegis-admin 是管理控制台网关（Admin 域）。
//
// 与 aegis-public 完全独立的进程与端口：管理面故障不影响用户下单续费，
// 用户面被打爆也不会挤掉管理员的处置能力（ARC-002）。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/api/admin"
	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/domain/billing"
	"github.com/aegispanel/aegis/internal/domain/certs"
	"github.com/aegispanel/aegis/internal/domain/content"
	"github.com/aegispanel/aegis/internal/domain/giftcard"
	"github.com/aegispanel/aegis/internal/domain/identity"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/domain/plugin"
	"github.com/aegispanel/aegis/internal/domain/subscription"
	"github.com/aegispanel/aegis/internal/domain/support"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/logging"
	"github.com/aegispanel/aegis/internal/platform/profiling"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/roundtrip"
	"github.com/aegispanel/aegis/internal/platform/server"
	"github.com/aegispanel/aegis/internal/platform/token"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	log := logging.New(cfg.Env, "aegis-admin")
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	// ctx 是启动阶段与后台循环的生命周期。开服前收到信号立即取消（中止启动）；
	// 开服后改由停机顺序决定：RunContext 先停接新请求、等在途请求跑完，
	// 返回之后才 stop()，后台循环这时才退出。
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopOnEarlySignal := context.AfterFunc(sigCtx, stop)
	closeResourcesOnReturn := true

	pool, err := db.OpenWithOptions(ctx, cfg.DatabaseURL, db.Options{
		MaxConns: cfg.DBMaxConns[config.DomainAdmin], MinConns: cfg.DBMinConns[config.DomainAdmin], StatsLog: log,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeResourcesOnReturn {
			pool.Close()
		}
	}()

	// 口令哈希（Argon2id 19 MiB/次）全局并发上限：后台登录、重认证、改密、替人重置、
	// 批量生成用户都在这个网关上，与 aegis-public 用同一组配置
	crypto.ConfigurePasswordHashing(cfg.PasswordHashConcurrency, cfg.PasswordHashQueueTimeout)

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("解析 Redis 连接串: %w", err)
	}
	rdb := redis.NewClient(redisOpt)
	// 往返记账：请求里的每条 Valkey 命令记进访问日志的 kv_rt（platform/roundtrip）
	rdb.AddHook(roundtrip.ValkeyHook{})
	defer func() {
		if closeResourcesOnReturn {
			_ = rdb.Close()
		}
	}()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("缓存不可达: %w", err)
	}

	// 用 admin 域自己的签名密钥。这把密钥与 public 域不同，
	// 是「令牌不可跨域使用」的物理保证（EXT-001）。
	issuer := token.NewIssuer(string(config.DomainAdmin),
		cfg.JWTSecrets[config.DomainAdmin], cfg.AccessTokenTTL)

	identitySvc := identity.NewService(pool, issuer, cfg.RefreshTokenTTL, cfg.MasterKey, !cfg.IsProduction())
	opsSvc := adminops.NewService(pool)
	contentSvc := content.New(pool)
	supportSvc := support.NewService(pool)
	nodeSigner, err := crypto.NewSigner(cfg.ConfigSigningSeed)
	if err != nil {
		return fmt.Errorf("初始化配置签名器: %w", err)
	}
	nodeSvc := nodefabric.NewService(pool, nodeSigner)

	// 信封加密器：审计里的来源 IP 落库前用它加密
	envelope, err := crypto.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("初始化信封加密: %w", err)
	}

	// 审计里的来源信息：哈希用于关联分析，密文供后台查看。
	//
	// 哈希函数与 identity 用的是同一个（crypto.HashIdentifier），
	// 否则同一个 IP 在两处会算出不同的值，按 IP 反查账号时会漏掉一半记录。
	audit.Configure(
		func(ip string) []byte { return crypto.HashIdentifier(cfg.MasterKey, ip) },
		func(b []byte) ([]byte, error) { return envelope.Seal(b, []byte("audit")) },
	)

	rtHub := realtime.NewHub(rdb, log)
	defer func() {
		if closeResourcesOnReturn {
			rtHub.Close()
		}
	}()
	nodeSvc.AttachRealtime(rtHub)

	// 管理端只用到 billing 的后台作业部分（佣金解冻），
	// 下单与支付仍然只在 public 网关里发生
	billingSvc := billing.NewService(pool, envelope)
	// 管理员手动标记已付、人工开单走的也是同一条履约路径，同样要通知节点；
	// 发布只经 nodefabric 的 NotifyUsersChanged 一处
	billingSvc.SetUsersChangedNotifier(nodeSvc.NotifyUsersChanged)
	// 后台「向渠道查单」要调渠道查询接口；查到已付交给上面这个结算服务补记。
	// 管理端不建收银台，publicBaseURL 只是装配需要（拼回调地址），用不到
	paymentSvc := billing.NewPaymentService(billingSvc, pool, envelope, cfg.MasterKey,
		cfg.PublicBaseURL, !cfg.IsProduction())
	// 管理端只用它把到点的定时公告转正、给工单回复排队，不投递任何消息，所以没有 sender
	// 收件人哈希用通知专用盐，与 public 网关同一个，同一收件人两边算出同一个值
	notifySvc := notify.New(pool, log, crypto.NotifyRecipientSalt(cfg.MasterKey))
	// 客服非内部回复在同一事务里给提单人排 ticket.replied（R115）。本进程不跑派发
	// 循环，Kick 在这里是空操作，排好的通知由 public 网关的扫描循环投递
	supportSvc.SetReplyNotifier(notifySvc)
	appearanceSvc := appearance.New(pool)
	// 插件钩子在生产模式下拒绝内网地址；devMode 传 !IsProduction()，
	// 和支付渠道那边用的是同一个判据（SEC-007）。
	pluginSvc := plugin.New(pool, envelope, !cfg.IsProduction())
	// 礼品卡把「发什么」交回给计费域执行，自己只管「该不该发」
	giftCardSvc := giftcard.New(pool, log, billingSvc.GiftGranter())
	smtpProvider := notify.NewDBSMTPProvider(pool, envelope)

	// IP 归属地库。缺文件不算致命：后台照常可用，只是归属地列留空。
	// 风控明细里最要紧的是 IP 和时间，归属地是帮着判断的旁证。
	geoResolver, geoErr := geoip.Open(cfg.AdminGeoIPDB(), cfg.GeoIPIPv6DB)
	if geoErr != nil {
		log.Warn("IP 归属地库不可用，归属地列将留空",
			"path", cfg.AdminGeoIPDB(), "err", geoErr)
	} else {
		defer geoResolver.Close()
	}
	// 节点接入时用同一份库把公网 IP 自动识别成地区，填到 servers.region，
	// 管理员新建服务器时不必手抄地区。没配库就静默降级（region 留空）。
	nodeSvc.SetGeoIP(geoResolver)

	handler := admin.NewRouter(admin.Deps{
		GeoIP:          geoResolver,
		Realtime:       rtHub,
		Envelope:       envelope,
		Billing:        billingSvc,
		Payments:       paymentSvc,
		Content:        contentSvc,
		SMTPProvider:   smtpProvider,
		Notify:         notifySvc,
		GiftCard:       giftCardSvc,
		TelegramSender: notify.NewDynamicTelegramSender(pool, envelope, middleware.DefaultTenantID),
		Appearance:     appearanceSvc,
		Plugin:         pluginSvc,
		Cfg:            cfg, Pool: pool, Redis: rdb, Log: log,
		Issuer: issuer, Identity: identitySvc, Ops: opsSvc, Support: supportSvc, Node: nodeSvc,
		// salt 从主密钥派生，和 public 域算出来的是同一个值 —— 换发订阅
		// 凭据时两边写进去的哈希必须一致，否则用户拿到的新链接验不过。
		Subscription: subscription.New(pool, crypto.SubscriptionAuditSalt(cfg.MasterKey), envelope),
	})

	log.Info("AegisPanel Admin 控制台就绪", "addr", cfg.AdminAddr)

	// SLA 扫描：把首次响应超时的工单自动升级（OPS-001 验收「超时自动升级」）。
	//
	// 放在 admin 网关而不是单独的 worker 进程：面向低配单机部署，
	// 多一个常驻进程的代价大于收益；而 EscalateOverdue 本身是幂等的，
	// 将来拆成独立 worker 或换成 cron 也不需要改动业务代码。
	//
	// 六个循环都经 newLoopPacer 定节拍：首轮随机延迟、之后每轮 ±10% 抖动（pacer.go）。
	var workers sync.WaitGroup
	workers.Add(8)
	go func() {
		defer workers.Done()
		pace := newLoopPacer(5 * time.Minute)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			n, err := supportSvc.EscalateOverdue(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("工单 SLA 扫描失败", "error", err.Error())
			case n > 0:
				log.Warn("工单因首次响应超时被自动升级", "count", n)
			}
		}
	}()

	// 定时公告到点转正。
	//
	// 没有这一步，scheduled 状态会永远停在那里 —— 运营以为排好了
	// 周二早上的维护通知，实际上它一直没发出去。
	go func() {
		defer workers.Done()
		pace := newLoopPacer(time.Minute)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			n, err := notifySvc.PublishDueAnnouncements(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("定时公告发布失败", "error", err.Error())
			case n > 0:
				log.Info("定时公告已发布", "count", n)
			}
		}
	}()

	// 配额周期滚动：把 day / month 型配额滚到下一周期。
	//
	// 跑得比周期短得多是必须的：整点跑一次的话，一个月付流量包
	// 会在月初到点后最长等一小时才恢复 —— 那一小时里用户是断网的。
	go func() {
		defer workers.Done()
		pace := newLoopPacer(10 * time.Minute)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := billingSvc.RollQuotaPeriods(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("配额周期滚动失败", "error", err.Error())
			case n > 0:
				log.Info("配额已滚入新周期", "count", n)
			}
			nodeSvc.EnsureLivenessPatrol(ctx, middleware.DefaultTenantID, log) // 节点在线巡检（30 秒一轮，只起一次）
		}
	}()

	// 佣金解冻：把冻结期已过的佣金转成可提现。
	//
	// 一小时一次足够：冻结期以天计，用户不会盯着秒表等解冻。
	// 跑得太勤只是让一个纯写事务反复抢锁，对谁都没好处。
	go func() {
		defer workers.Done()
		pace := newLoopPacer(time.Hour)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			n, err := billingSvc.SettleMatured(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("佣金解冻失败", "error", err.Error())
			case n > 0:
				log.Info("佣金已解冻", "count", n)
			}
		}
	}()

	// 保留期清理：过期的在线记录（node_alive_ips，70 分钟前）、探针点（node_metrics，
	// 48 小时前）与流量汇总（节点 × uid 小时表 70 天、节点小时表与按天表 400 天）。它们都随
	// 节点上报增长，不清就让在线统计、节点列表与看板一天比一天慢。追加写的上报留档与订阅
	// 拉取日志保留 31 天（用户定），经各自的定义者函数分批删（00131）。
	//
	// 分批删：三个 Purge 每批一个短事务、每批行数与单次调用的批数都有上限，积压由下一轮接着清，
	// 不会一次删几十万行长时间持锁。清理一小时一次（w12period）：读路径上这些表都带时间窗
	// （在线记录最多看 60 分钟，探针曲线最多 24 小时，流量汇总最多 61 天），晚清一小时只是多留
	// 一小时的行，不影响读数；原先每 10 分钟一轮，静默时九成是空转。
	//
	// 节拍仍是 10 分钟：同一个循环里的两项按天汇总要及时，各自在库外先判断要不要动库，
	// 没事可做的节拍几乎不碰库——
	//   - 行为趋势（00114）：最近 2 个已结束日里，只算还没定稿、读路径也用不上的行；
	//   - 节点 × uid 按天流量（00133）：进度记在进程内，启动与每个 UTC 日界之后才核对一次库。
	// 清理挂在 intervalGate 后面，到点（离上次成功满一小时）才跑；这一轮有失败就不记，下一拍重试。
	go func() {
		defer workers.Done()
		pace := newLoopPacer(retentionTick)
		defer pace.Stop()
		purgeGate := newIntervalGate(retentionPurgeEvery, retentionTick)
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			// 按天汇总排在清理之前：它们是读路径在用的，清积压占满本轮时限时不能挤掉它们
			// 行为趋势每轮不固定行数，不记日志
			_, activityErr := opsSvc.RefreshActivityDaily(sctx, middleware.DefaultTenantID)
			trafficDaily, trafficDailyErr := nodeSvc.RefreshTrafficDaily(sctx, middleware.DefaultTenantID)
			purgeDue := purgeGate.due(time.Now())
			var purge retentionPurge
			if purgeDue {
				purge.alive, purge.aliveErr = nodeSvc.PurgeStaleAlive(sctx, middleware.DefaultTenantID)
				purge.metrics, purge.metricsErr = nodeSvc.PurgeMetrics(sctx, middleware.DefaultTenantID, nodefabric.MetricsRetentionHours)
				purge.rollups, purge.rollupsErr = nodeSvc.PurgeTrafficRollups(sctx, middleware.DefaultTenantID)
				purge.activity, purge.activityErr = opsSvc.PurgeActivityDaily(sctx, middleware.DefaultTenantID)
				// 追加写表的 31 天保留期（w5retain，00131）：排在上面几项之后，清积压占满本轮时限时
				// 不挤掉在线记录与探针点的清理
				purge.reports, purge.reportsErr = nodeSvc.PurgeTrafficReports(sctx, middleware.DefaultTenantID)
				purge.fetchLogs, purge.fetchLogsErr = subscription.PurgeFetchLog(sctx, pool, middleware.DefaultTenantID)
			}
			cancel()
			if activityErr != nil {
				log.Error("行为趋势按天汇总失败", "error", activityErr.Error())
			}
			if trafficDailyErr != nil {
				log.Error("流量按天汇总失败", "error", trafficDailyErr.Error(), "written", trafficDaily)
			}
			if trafficDaily > 0 {
				log.Info("流量按天汇总完成", "traffic_daily_rows", trafficDaily)
			}
			if purgeDue {
				purge.report(log)
				if purge.ok() {
					purgeGate.done(time.Now())
				}
			}
		}
	}()

	// 订阅过期扫描（w5expiry）：到期的订阅改成 expired、写过期事件与 subscription.expired
	// 钩子；过期满 30 天的关闭原地续费窗口并吊销凭据（billing/expire.go）。停发不靠它
	// （节点名单与订阅拉取按周期末现算），它管的是状态、事件和之后的续费口径；到期通知
	// 与召回由 public 网关的通知扫描按 expired 状态发。一分钟一轮：门户「已过期」与
	// 续费入口最多晚一分钟。
	go func() {
		defer workers.Done()
		pace := newLoopPacer(time.Minute)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, time.Minute)
			res, err := billingSvc.ScanExpiredSubscriptions(sctx, middleware.DefaultTenantID)
			cancel()
			switch {
			case err != nil:
				log.Error("订阅过期扫描失败", "error", err.Error(),
					"expired", res.Expired, "closed", res.Closed)
			case res.Expired > 0 || res.Closed > 0:
				log.Info("订阅过期扫描完成", "expired", res.Expired,
					"renewal_closed", res.Closed, "revoked_credentials", res.RevokedCredentials)
			}
		}
	}()

	// 批量生成账号的后台任务（w5account）：POST v1/users/bulk/generate 只登记任务，这里逐个
	// 生成。一次只占 1 个 Argon2 名额、名额排不上就等，不挡登录。登记任务时 opsSvc 在进程内
	// 叫醒这个循环（w12period），不再每 3 秒问一次库；另有一条慢轮询兜底别的实例登记的任务、
	// 租约过期被丢下的任务和刚启动时已排着的任务。叫醒后连着做，做到没有可认领的任务为止；
	// 顺带清掉超过 24 小时的结果密文。多实例时靠租约与 SKIP LOCKED 分活，停机或挂掉的任务
	// 租约一过就被接着做。
	go func() {
		defer workers.Done()
		gen := opsSvc.NewUserGenerationWorker(envelope, log)
		wake := opsSvc.UserGenerationWake()
		pace := newLoopPacer(adminops.UserGenerationPollEvery)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			case <-wake:
			}
			for {
				sctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
				ran, err := gen.RunOnce(sctx, middleware.DefaultTenantID)
				cancel()
				if err != nil && ctx.Err() == nil {
					log.Error("批量生成账号任务失败", "error", err.Error())
				}
				// 做完一个就接着看有没有下一个；出错或没活时回到等待，不空转
				if err != nil || !ran || ctx.Err() != nil {
					break
				}
			}
		}
	}()

	// 节点证书巡检（w9cert，P2）：排到期的签发与续期订单、隔几分钟刷新 ARI 窗口与到期等级，每轮认领
	// 至多一张订单用 lego DNS-01 签掉。订单是租约行（部分唯一索引 + SKIP LOCKED），多实例不会重复签；
	// lego 没有 context，签发跑在另一个协程里，停机时不等它，租约一过由下一次启动接着做。
	go func() {
		defer workers.Done()
		certWorker := certs.NewService(pool, envelope, certs.Options{
			DirectoryOverride: cfg.ACME.DirectoryOverride, TrustedRoots: cfg.ACME.TrustedRoots, Log: log,
			LibraryEnv: cfg.ACME.LibraryEnv,
		}).NewWorker(log)
		pace := newLoopPacer(30 * time.Second)
		defer pace.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-pace.C():
			}
			sctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
			_, err := certWorker.RunOnce(sctx, middleware.DefaultTenantID)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Error("证书巡检失败", "error", err.Error())
			}
		}
	}()

	// pprof 诊断端口：默认关闭，AEGIS_ADMIN_PPROF_ADDR 设了回环地址才开（独立端口，
	// 不经 nginx、不挂业务路由），随网关停机关闭
	pprofSrv, err := profiling.Start(cfg.PprofAddrs[config.DomainAdmin], log)
	if err != nil {
		return err
	}
	defer pprofSrv.Close()

	stopOnEarlySignal()
	serverErr := server.RunContext(sigCtx, server.Options{
		Addr:            cfg.AdminAddr,
		Handler:         handler,
		Log:             log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	stop()
	drainErr := waitForAdminWorkers(&workers, cfg.ShutdownTimeout)
	if drainErr != nil {
		// A timed-out worker may still own a database connection or use Redis.
		// Do not race it with graceful closers (pgxpool.Close can itself wait
		// forever); main will return this fatal error and os.Exit will reclaim
		// process resources.
		closeResourcesOnReturn = false
		log.Error("admin background workers did not stop before shutdown deadline",
			"error", drainErr.Error())
	}
	return errors.Join(serverErr, drainErr)
}

var errAdminWorkerDrainTimeout = errors.New("admin worker drain timed out")

func waitForAdminWorkers(workers *sync.WaitGroup, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 20 * time.Second
	}

	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return fmt.Errorf("%w after %s", errAdminWorkerDrainTimeout, timeout)
	}
}
