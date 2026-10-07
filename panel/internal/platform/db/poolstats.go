package db

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolStatsInterval 是连接池统计日志的周期。
const PoolStatsInterval = time.Minute

// poolStatsSource 是 *pgxpool.Pool 的统计面，便于不连库测试。
type poolStatsSource interface {
	Stat() *pgxpool.Stat
}

// poolStatsSnapshot 是一次采样里的累计计数，用来算本周期的增量。
type poolStatsSnapshot struct {
	acquires         int64
	acquireDuration  time.Duration
	emptyAcquires    int64
	emptyAcquireWait time.Duration
	canceledAcquires int64
	newConns         int64
	lifetimeDestroys int64
	idleDestroys     int64
}

func snapshotOf(s *pgxpool.Stat) poolStatsSnapshot {
	return poolStatsSnapshot{
		acquires:         s.AcquireCount(),
		acquireDuration:  s.AcquireDuration(),
		emptyAcquires:    s.EmptyAcquireCount(),
		emptyAcquireWait: s.EmptyAcquireWaitTime(),
		canceledAcquires: s.CanceledAcquireCount(),
		newConns:         s.NewConnsCount(),
		lifetimeDestroys: s.MaxLifetimeDestroyCount(),
		idleDestroys:     s.MaxIdleDestroyCount(),
	}
}

// poolStatsAttrs 把一次采样（瞬时值 + 与上次相比的增量）摊成日志字段。
//
// 看什么：empty_acquires（取连接时池里没有空闲、要等或要新建）与 acquire_wait_ms
// 是「池子不够」的直接证据；acquired 贴着 max 却 empty_acquires 为 0，说明只是
// 连接都被轮着用、没有排队（5k-r3 node 池「14.78/15」就是这样被误读的）。
// lifetime_destroys / new_conns 成批出现就是同批到期重建。
func poolStatsAttrs(s *pgxpool.Stat, prev, cur poolStatsSnapshot) []any {
	acquires := cur.acquires - prev.acquires
	var avgAcquire time.Duration
	if acquires > 0 {
		avgAcquire = (cur.acquireDuration - prev.acquireDuration) / time.Duration(acquires)
	}
	return []any{
		"total", s.TotalConns(),
		"idle", s.IdleConns(),
		"acquired", s.AcquiredConns(),
		"constructing", s.ConstructingConns(),
		"max", s.MaxConns(),
		"acquires", acquires,
		"acquire_avg_us", avgAcquire.Microseconds(),
		"empty_acquires", cur.emptyAcquires - prev.emptyAcquires,
		"acquire_wait_ms", (cur.emptyAcquireWait - prev.emptyAcquireWait).Milliseconds(),
		"canceled_acquires", cur.canceledAcquires - prev.canceledAcquires,
		"new_conns", cur.newConns - prev.newConns,
		"lifetime_destroys", cur.lifetimeDestroys - prev.lifetimeDestroys,
		"idle_destroys", cur.idleDestroys - prev.idleDestroys,
	}
}

// startPoolStatsLog 每 every 打一行连接池统计，返回的函数停掉它并等它退出。
// 本周期一次取连接都没有、也没有建连与销毁时不打，免得空闲进程刷日志。
func startPoolStatsLog(src poolStatsSource, log *slog.Logger, every time.Duration) func() {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		prev := snapshotOf(src.Stat())
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			stat := src.Stat()
			cur := snapshotOf(stat)
			if cur != prev {
				log.Info("数据库连接池统计", poolStatsAttrs(stat, prev, cur)...)
			}
			prev = cur
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			cancel()
			wg.Wait()
		})
	}
}
