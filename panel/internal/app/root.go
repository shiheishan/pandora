// Package app 提供 pandora 的 cobra 根命令与子命令装配。
package app

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/db"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/redisx"
	"github.com/redis/go-redis/v9"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

// runtime 是 serve / worker 共用的已打开资源。
type runtime struct {
	cfg   *config.Config
	log   *zap.Logger
	db    *db.DB
	redis *redis.Client
}

// Execute 运行根命令，收到信号时取消上下文。
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	defaultConfig := ""
	if _, err := os.Stat("/etc/pandora/config.yaml"); err == nil {
		defaultConfig = "/etc/pandora/config.yaml"
	}

	var configPath string
	root := &cobra.Command{
		Use:           "pandora",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "配置文件路径（秘密须用 PANDORA_* 环境变量）")

	root.AddCommand(newServeCmd(&configPath))
	root.AddCommand(newWorkerCmd(&configPath))
	root.AddCommand(newMigrateCmd(&configPath))
	root.AddCommand(newAdminCmd(&configPath))
	root.AddCommand(newBackupCmd(&configPath))

	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitCode(err)
	}
	return 0
}

// loadBase 加载配置并构造 logger。
func loadBase(path string) (*config.Config, *zap.Logger, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return nil, nil, fmt.Errorf("加载配置: %w", err)
	}
	l, err := plog.New(cfg.Log, cfg.Env)
	if err != nil {
		return nil, nil, fmt.Errorf("初始化日志: %w", err)
	}
	zap.ReplaceGlobals(l)
	return cfg, l, nil
}

// openRuntime 加载配置并打开数据库与 Redis。
func openRuntime(ctx context.Context, path string) (*runtime, error) {
	cfg, l, err := loadBase(path)
	if err != nil {
		return nil, err
	}

	gdb, err := db.Open(ctx, db.Options{
		URL:      cfg.Database.URL.Reveal(),
		MaxConns: cfg.Database.MaxConns,
		MinConns: cfg.Database.MinConns,
		Logger:   l,
	})
	if err != nil {
		_ = l.Sync()
		return nil, fmt.Errorf("打开数据库: %w", err)
	}

	redisClient, err := redisx.New(cfg.Redis)
	if err != nil {
		_ = gdb.Close()
		_ = l.Sync()
		return nil, fmt.Errorf("创建 Redis 客户端: %w", err)
	}
	if err := redisx.Ping(ctx, redisClient); err != nil {
		_ = redisClient.Close()
		_ = gdb.Close()
		_ = l.Sync()
		return nil, fmt.Errorf("连接 Redis: %w", err)
	}

	if cfg.IsProduction() {
		if err := gdb.CheckRuntimeRole(ctx); err != nil {
			_ = redisClient.Close()
			_ = gdb.Close()
			_ = l.Sync()
			return nil, fmt.Errorf("检查数据库运行角色: %w", err)
		}
	}

	return &runtime{
		cfg:   cfg,
		log:   l,
		db:    gdb,
		redis: redisClient,
	}, nil
}

func (r *runtime) close() {
	if r == nil {
		return
	}
	if r.redis != nil {
		_ = r.redis.Close()
	}
	if r.db != nil {
		_ = r.db.Close()
	}
	if r.log != nil {
		_ = r.log.Sync()
	}
}
