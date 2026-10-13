package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	audittask "github.com/aegispanel/aegis/internal/module/audit/task"
	certstask "github.com/aegispanel/aegis/internal/module/certs/task"
	commissiontask "github.com/aegispanel/aegis/internal/module/commission/task"
	idemtask "github.com/aegispanel/aegis/internal/module/idempotency/task"
	identitytask "github.com/aegispanel/aegis/internal/module/identity/task"
	nodetask "github.com/aegispanel/aegis/internal/module/node/task"
	notifytask "github.com/aegispanel/aegis/internal/module/notify/task"
	ordertask "github.com/aegispanel/aegis/internal/module/order/task"
	paymenttask "github.com/aegispanel/aegis/internal/module/payment/task"
	plugintask "github.com/aegispanel/aegis/internal/module/plugin/task"
	subtask "github.com/aegispanel/aegis/internal/module/subscription/task"
	supporttask "github.com/aegispanel/aegis/internal/module/support/task"
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

	redisOpt, err := asynqRedis(r.cfg)
	if err != nil {
		return err
	}
	svc, err := buildServices(r)
	if err != nil {
		return err
	}
	defer svc.close()
	go svc.settings.WatchSwitches(ctx)

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
	if err := jobs.Register(mux, sched, modules(svc)...); err != nil {
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
func modules(svc *services) []jobs.Module {
	return []jobs.Module{
		audittask.New(svc.audit),
		idemtask.New(svc.idem),
		identitytask.New(svc.identity),
		notifytask.New(svc.notify),
		paymenttask.New(svc.payment),
		ordertask.New(svc.order),
		commissiontask.New(svc.commission),
		subtask.New(svc.sub),
		nodetask.New(svc.node),
		supporttask.New(svc.support),
		certstask.New(svc.certs),
		plugintask.New(svc.plugin),
	}
}
