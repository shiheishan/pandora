package db

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// 连接池统计：有取连接活动的周期打一行（含排队与销毁的增量），停掉后不再打。
// 用一个连不上的池子制造「取连接失败」，不需要真库。
func TestPoolStatsLogReportsAcquireActivity(t *testing.T) {
	cfg, err := pgxpool.ParseConfig("postgres://stats@127.0.0.1:1/none?connect_timeout=1")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var buf lockedBuffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	stop := startPoolStatsLog(pool, log, 20*time.Millisecond)

	// 空闲周期不打日志
	time.Sleep(60 * time.Millisecond)
	if strings.Contains(buf.String(), "数据库连接池统计") {
		t.Fatalf("idle pool must not log: %s", buf.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	if c, err := pool.Acquire(ctx); err == nil {
		c.Release()
		t.Fatal("acquire against a closed port unexpectedly succeeded")
	}
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), "数据库连接池统计") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stop() // 可重复调用
	out := buf.String()
	for _, want := range []string{"数据库连接池统计", "empty_acquires=", "acquire_wait_ms=", "canceled_acquires=", "lifetime_destroys=", "new_conns=1", "max="} {
		if !strings.Contains(out, want) {
			t.Fatalf("pool stats log missing %q:\n%s", want, out)
		}
	}
	after := len(out)
	time.Sleep(60 * time.Millisecond)
	if len(buf.String()) != after {
		t.Fatal("pool stats log kept writing after stop")
	}
}
