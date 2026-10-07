// Command aegis-node 是节点控制面网关（Node 域）。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/api/node"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/middleware"
	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/logging"
	"github.com/aegispanel/aegis/internal/platform/profiling"
	"github.com/aegispanel/aegis/internal/platform/realtime"
	"github.com/aegispanel/aegis/internal/platform/server"
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
	log := logging.New(cfg.Env, "aegis-node")
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()
	// ctx 是启动阶段与后台循环（nonce 清理）的生命周期，写法与 public / admin 相同：开服前收到
	// 信号立即取消（中止启动）；开服后改由停机顺序决定：RunContext 先停接新请求、等在途请求
	// （流量上报、配置拉取）跑完，返回之后才 stop()，再限时 join 后台循环、交给 defer 关资源。
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	stopOnEarlySignal := context.AfterFunc(sigCtx, stop)
	closeResourcesOnReturn := true

	pool, err := db.OpenWithOptions(ctx, cfg.DatabaseURL, db.Options{
		MaxConns: cfg.DBMaxConns[config.DomainNode], MinConns: cfg.DBMinConns[config.DomainNode], StatsLog: log,
	})
	if err != nil {
		return err
	}
	defer func() {
		if closeResourcesOnReturn {
			pool.Close()
		}
	}()

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer rdb.Close()
	// Valkey 不是节点网关的硬依赖：节点的 REST 路径不用它（nonce 认领出错回落 PG），
	// 事件流的跨进程信号断了也只是退回轮询。启动时连不上只告警、照常起服务，
	// 否则 Valkey 一挂、aegis-node 一重启，所有节点都拿 502。
	pingCtx, cancelPing := context.WithTimeout(ctx, 3*time.Second)
	if err := rdb.Ping(pingCtx).Err(); err != nil {
		log.Warn("Valkey 暂不可用，节点网关降级启动：nonce 认领走 PG，配置推送退回轮询",
			"error", err.Error())
	}
	cancelPing()
	rtHub := realtime.NewHub(rdb, log)
	defer rtHub.Close()

	// 配置签名密钥与 Client 域共用同一把：Agent 和客户端验的是同一个平台身份，
	// 分成两把只会多一套轮换流程而不增加任何隔离效果。
	signer, err := crypto.NewSigner(cfg.ConfigSigningSeed)
	if err != nil {
		return fmt.Errorf("初始化配置签名器: %w", err)
	}

	// 信封加密器：只给审计里的来源 IP 加密用
	envelope, err := crypto.NewEnvelope(cfg.MasterKey)
	if err != nil {
		return fmt.Errorf("初始化信封加密: %w", err)
	}
	// 审计里的来源信息：哈希用于关联分析，密文供后台查看。与 public、admin 两个
	// 网关逐字相同。不注入时 audit.Write 从 context 拿到了来源 IP 却既不算哈希
	// 也不加密，节点入网这类记录的 source_ip_hash 与 source_ip_enc 都为空，
	// 按 IP 反查时节点侧整段缺席。
	audit.Configure(
		func(ip string) []byte { return crypto.HashIdentifier(cfg.MasterKey, ip) },
		func(b []byte) ([]byte, error) { return envelope.Seal(b, []byte("audit")) },
	)

	nodeService := nodefabric.NewService(pool, signer)
	// 签名请求的 nonce 先在 Valkey 认领（SET NX PX），出错回落 PG。PG 里还有未过期的
	// nonce（上次运行回落过）时，保留期内两边都认领，重放照样被拦下。
	nodeService.SetNonceStore(valkeyNonceStore{rdb: rdb}, log)
	primeCtx, cancelPrime := context.WithTimeout(ctx, 5*time.Second)
	nodeService.PrimeNonceFallback(primeCtx, middleware.DefaultTenantID)
	cancelPrime()
	// 下发给节点的拉用户节拍（AEGIS_NODE_PULL_INTERVAL，缺省 15 秒）
	if err := nodeService.SetNodePullInterval(cfg.NodePullInterval); err != nil {
		return err
	}
	// 节点首次接入把公网 IP 自动识别成地区填 servers.region。
	// 缺库不致命：自动识别降级为空，接入照常。
	// 与 aegis-admin 不同，这里 AEGIS_GEOIP_DB 没有缺省路径：未设置就不开。
	if geoResolver, geoErr := geoip.Open(cfg.GeoIPDB, cfg.GeoIPIPv6DB); geoErr == nil {
		defer geoResolver.Close()
		nodeService.SetGeoIP(geoResolver)
	} else if geoErr != geoip.ErrNoDatabase {
		log.Warn("IP 归属地库不可用，服务器地区将不会自动识别",
			"path", cfg.GeoIPDB, "err", geoErr)
	}
	// 节点接入比对本次发布钉死的 NativeCore 摘要与版本；生产缺失即拒绝接入。
	nodeService.SetReleaseBinding(nodefabric.ReleaseBinding{
		Production:     cfg.IsProduction(),
		ArtifactSHA256: cfg.NativeArtifactSHA256,
		Version:        cfg.NativeReleaseVersion,
	})
	if len(cfg.PreviousConfigSigningSeed) > 0 {
		previousSigner, err := crypto.NewSigner(cfg.PreviousConfigSigningSeed)
		if err != nil {
			return fmt.Errorf("initialize previous config signer: %w", err)
		}
		if err := nodeService.SetPreviousConfigSigner(previousSigner); err != nil {
			return fmt.Errorf("configure previous config signer: %w", err)
		}
	}
	// 事件流的连接注册表。只在本进程内存里——面板多副本时，节点连在
	// 哪个副本上就只能收到那个副本推的消息。这是有意接受的：轮询那条路
	// 还在，最坏情况就是回到没有推送时的节奏。
	stream := nodefabric.NewStreamHub()
	nodeService.AttachStream(stream)
	nodeService.AttachRealtime(rtHub)
	// 节点链路缓存（按池的用户集、签名身份），由下发纪元保证改完即生效（迁移 00101）。
	nodeService.EnableNodeCaches()

	var workers sync.WaitGroup
	workers.Add(1)
	// 签名请求 nonce 的过期清理。原先每个请求顺手删一批，并发请求争同一批行；
	// 防重放只靠主键冲突，清理晚几分钟不影响判定，只影响表的大小。
	go func() {
		defer workers.Done()
		runNoncePurge(ctx, nodeService, log)
	}()

	handler := node.NewRouter(node.Deps{
		Cfg: cfg, Pool: pool, Log: log,
		Node:       nodeService,
		NodeStream: stream,
	})

	log.Info("AegisPanel Node 控制面就绪",
		"addr", cfg.NodeAddr, "config_key_id", signer.KeyID())

	// pprof 诊断端口：默认关闭，AEGIS_NODE_PPROF_ADDR 设了回环地址才开（独立端口，
	// 不经 nginx、不挂业务路由），随网关停机关闭
	pprofSrv, err := profiling.Start(cfg.PprofAddrs[config.DomainNode], log)
	if err != nil {
		return err
	}
	defer pprofSrv.Close()

	stopOnEarlySignal()
	serverErr := server.RunContext(sigCtx, server.Options{
		Addr:            cfg.NodeAddr,
		Handler:         handler,
		Log:             log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
	stop()
	drainErr := waitForNodeWorkers(&workers, cfg.ShutdownTimeout)
	if drainErr != nil {
		// 没退出的后台循环可能还拿着库连接：不和它抢着关连接池，交给进程退出回收。
		closeResourcesOnReturn = false
		log.Error("node background workers did not stop before shutdown deadline",
			"error", drainErr.Error())
	}
	return errors.Join(serverErr, drainErr)
}

// valkeyNonceStore 把签名请求的 nonce 认领交给 Valkey：SET key 1 NX PX ttl。
type valkeyNonceStore struct{ rdb *redis.Client }

func (v valkeyNonceStore) ClaimNonce(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	return v.rdb.SetNX(ctx, key, "1", ttl).Result()
}

// noncePurgeInterval 是过期 nonce 的清理节拍。每条 nonce 至少留 11 分钟，一分钟
// 一扫足够让表维持在「最近十几分钟的签名请求数」这个量级。
const noncePurgeInterval = time.Minute

func runNoncePurge(ctx context.Context, svc *nodefabric.Service, log *slog.Logger) {
	t := time.NewTicker(noncePurgeInterval)
	defer t.Stop()
	for {
		runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		n, err := svc.PurgeExpiredNonces(runCtx, middleware.DefaultTenantID)
		cancel()
		switch {
		case err != nil && ctx.Err() == nil:
			log.Error("清理过期的节点请求 nonce 失败", "error", err.Error(), "已删除", n)
		case n > 0:
			log.Debug("已清理过期的节点请求 nonce", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

var errNodeWorkerDrainTimeout = errors.New("node worker drain timed out")

func waitForNodeWorkers(workers *sync.WaitGroup, timeout time.Duration) error {
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
		return fmt.Errorf("%w after %s", errNodeWorkerDrainTimeout, timeout)
	}
}
