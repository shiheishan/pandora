package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/jobs"
	"github.com/hibiken/asynq"
	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func newWorkerCmd(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "worker",
		Short: "启动异步任务 worker 与定时调度",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runWorker(cmd.Context(), *configPath)
		},
	}
}

func runWorker(ctx context.Context, configPath string) error {
	r, err := openRuntime(ctx, configPath)
	if err != nil {
		return err
	}
	defer r.close()

	redisOpt, err := asynq.ParseRedisURI(r.cfg.Redis.URL.Reveal())
	if err != nil {
		return fmt.Errorf("解析 Redis URL: %w", err)
	}

	zapAsynq := jobs.ZapLogger{SugaredLogger: r.log.Sugar()}
	cfg := r.cfg.Worker

	srv := asynq.NewServer(redisOpt, asynq.Config{
		Concurrency:     cfg.Concurrency,
		Queues:          map[string]int{"critical": 6, "default": 3, "low": 1},
		ShutdownTimeout: cfg.ShutdownTimeout,
		Logger:          zapAsynq,
	})

	sched := asynq.NewScheduler(redisOpt, &asynq.SchedulerOpts{
		Location: time.UTC,
		Logger:   zapAsynq,
	})

	mux := asynq.NewServeMux()
	mux.Use(jobs.Middleware(r.log))
	if err := jobs.Register(mux, sched, modules()...); err != nil {
		return fmt.Errorf("注册任务模块: %w", err)
	}

	var pprofSrv *http.Server
	if addr := cfg.PprofAddr; addr != "" {
		pprofSrv = startPprofServer(addr)
	}

	if err := srv.Start(mux); err != nil {
		return fmt.Errorf("启动 asynq worker: %w", err)
	}
	if err := sched.Start(); err != nil {
		srv.Shutdown()
		return fmt.Errorf("启动 asynq 调度器: %w", err)
	}

	if pprofSrv != nil {
		go func() {
			if err := pprofSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				r.log.Warn("pprof 监听", zap.Error(err))
			}
		}()
	}

	r.log.Info("worker started", zap.Int("concurrency", cfg.Concurrency))

	<-ctx.Done()

	sched.Shutdown()
	srv.Shutdown()
	if pprofSrv != nil {
		shutCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := pprofSrv.Shutdown(shutCtx); err != nil {
			r.log.Warn("关闭 pprof", zap.Error(err))
		}
	}
	return nil
}

// modules 返回已迁移的业务任务模块；迁移时在此登记。
func modules() []jobs.Module {
	return nil
}
