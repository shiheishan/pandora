// Command aegis-node 是节点控制面网关（Node 域）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/aegispanel/aegis/internal/api/node"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/config"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/geoip"
	"github.com/aegispanel/aegis/internal/platform/logging"
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
	ctx := context.Background()

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	redisOpt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("parse Redis URL: %w", err)
	}
	rdb := redis.NewClient(redisOpt)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis unavailable: %w", err)
	}
	rtHub := realtime.NewHub(rdb, log)
	defer rtHub.Close()

	// 配置签名密钥与 Client 域共用同一把：Agent 和客户端验的是同一个平台身份，
	// 分成两把只会多一套轮换流程而不增加任何隔离效果。
	signer, err := crypto.NewSigner(cfg.ConfigSigningSeed)
	if err != nil {
		return fmt.Errorf("初始化配置签名器: %w", err)
	}

	nodeService := nodefabric.NewService(pool, signer)
	// 节点首次接入把公网 IP 自动识别成地区填 servers.region。
	// 缺库不致命：自动识别降级为空，接入照常。
	if geoResolver, geoErr := geoip.Open(os.Getenv("AEGIS_GEOIP_DB")); geoErr == nil {
		defer geoResolver.Close()
		nodeService.SetGeoIP(geoResolver)
	} else if geoErr != geoip.ErrNoDatabase {
		log.Warn("IP 归属地库不可用，服务器地区将不会自动识别",
			"path", os.Getenv("AEGIS_GEOIP_DB"), "err", geoErr)
	}
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

	handler := node.NewRouter(node.Deps{
		Cfg: cfg, Pool: pool, Log: log,
		Node:       nodeService,
		NodeStream: stream,
	})

	log.Info("AegisPanel Node 控制面就绪",
		"addr", cfg.NodeAddr, "config_key_id", signer.KeyID())

	return server.Run(server.Options{
		Addr:            cfg.NodeAddr,
		Handler:         handler,
		Log:             log,
		ShutdownTimeout: cfg.ShutdownTimeout,
	})
}
