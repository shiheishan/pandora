package nodefabric

// 节点在线巡检：「在线 → 离线」翻转时补发一条节点变更通知。
//
// 迁移 00110 之后，心跳写入不再触发 nodes.changed，「离线 → 在线」由触发器按两次心跳的
// 间隔判出来（间隔达到 NodeStaleAfter 就通知）。反方向没有写入可挂：节点停止心跳，
// 库里什么都不发生，界面上的「离线」只是时间流逝后读出来的。这里每 30 秒看一眼，
// 把上一轮之后心跳刚好跨过窗口的节点各通知一次。
//
// 两个窗口都要看，因为后台列表上两处会跟着变：
//   - NodeStaleAfter（90 秒）：列表的 stale，「在线 / 离线」与侧栏「离线节点」任务
//   - NodeDeliveryFreshWindow（10 分钟）：下发判定的新鲜窗口，delivery_note 会变
//
// 「跨过」用区间判：上一轮水位 since、本轮 until（都取库时间），心跳时刻落在
// (since - 窗口, until - 窗口] 里的节点，恰好在这两轮之间变旧，每个窗口每个节点只命中一轮，
// 所以每次翻转只发一次。通知走与触发器相同的 pg_notify('aegis_change')，载荷形状相同，
// 由 aegis-public 的监听器转成 nodes.changed（只推管理端，见 realtime.channelsFor）。
//
// 进程重启丢掉水位时，第一轮从 now - 巡检间隔 起算：重启期间的翻转不补发，
// 后台列表另有 30 秒定时轮询兜底；不往回追，免得重启时给成百上千个早已离线的节点各发一条。

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

const (
	// NodeStaleAfter 是后台判「离线」的心跳超时：与节点列表的 stale、adminops 的在线计数、
	// 迁移 00110 的「离线 → 在线」间隔是同一个 90 秒。
	NodeStaleAfter = 90 * time.Second
	// NodeDeliveryFreshWindow 是下发判定的心跳新鲜窗口，等于 subscription.HeartbeatFreshWindow
	// （nodefabric 不能 import subscription，由外部测试包钉住两者相等）。
	NodeDeliveryFreshWindow = 10 * time.Minute
	// livenessPatrolInterval 是巡检间隔：翻转最多晚这么久被推到后台。
	livenessPatrolInterval = 30 * time.Second
)

// livenessNotice 与 app.notify_change() 的载荷同形（tbl / op / tenant / user / id）。
type livenessNotice struct {
	Table  string  `json:"tbl"`
	Op     string  `json:"op"`
	Tenant string  `json:"tenant"`
	User   *string `json:"user"`
	ID     string  `json:"id"`
}

// PatrolNodeLiveness 给 (since, 现在] 之间心跳跨过 NodeStaleAfter 或 NodeDeliveryFreshWindow 的节点
// 各发一条节点变更通知，返回本轮水位（库时间，作为下一轮的 since）与发出的条数。
// since 为零值时从「现在 - 巡检间隔」起算。通知随事务提交投递。
func (s *Service) PatrolNodeLiveness(ctx context.Context, tenantID string, since time.Time) (time.Time, int, error) {
	var sinceArg *time.Time
	if !since.IsZero() {
		sinceArg = &since
	}
	var until time.Time
	var notices []string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var ids []string
		// 已退役、已销毁的节点不参与：列表上它们的状态与心跳无关
		if err := tx.QueryRow(ctx, `
			WITH clock AS MATERIALIZED (
			  SELECT now() AS until,
			         coalesce($2::timestamptz, now() - make_interval(secs => $3)) AS since
			)
			SELECT c.until,
			       coalesce((
			         SELECT array_agg(n.id::text ORDER BY n.id)
			           FROM nodes n
			          WHERE n.tenant_id = $1
			            AND n.status <> 'destroyed' AND n.serving_status <> 'retired'
			            AND n.last_heartbeat_at IS NOT NULL
			            AND ((n.last_heartbeat_at >  c.since - make_interval(secs => $4)
			                  AND n.last_heartbeat_at <= c.until - make_interval(secs => $4))
			              OR (n.last_heartbeat_at >  c.since - make_interval(secs => $5)
			                  AND n.last_heartbeat_at <= c.until - make_interval(secs => $5)))
			       ), '{}')
			  FROM clock c`,
			tenantID, sinceArg, livenessPatrolInterval.Seconds(),
			NodeStaleAfter.Seconds(), NodeDeliveryFreshWindow.Seconds(),
		).Scan(&until, &ids); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		notices = make([]string, 0, len(ids))
		for _, id := range ids {
			raw, err := json.Marshal(livenessNotice{Table: "nodes", Op: "UPDATE", Tenant: tenantID, ID: id})
			if err != nil {
				return err
			}
			notices = append(notices, string(raw))
		}
		_, err := tx.Exec(ctx, `SELECT pg_notify('aegis_change', p) FROM unnest($1::text[]) AS p`, notices)
		return err
	})
	if err != nil {
		return time.Time{}, 0, err
	}
	return until, len(notices), nil
}

// livenessPatrols 记着哪些 Service 已经起了巡检：EnsureLivenessPatrol 被反复调用也只起一次。
var livenessPatrols sync.Map

// EnsureLivenessPatrol 为这个 Service 起节点在线巡检（每 30 秒一轮），重复调用是空操作；
// ctx 结束巡检即停。aegis-admin 在「配额周期滚动」循环里调用它（那个循环 10 分钟一轮，
// 第一轮在启动时就跑）。
func (s *Service) EnsureLivenessPatrol(ctx context.Context, tenantID string, log *slog.Logger) {
	if _, started := livenessPatrols.LoadOrStore(s, struct{}{}); started {
		return
	}
	go s.runLivenessPatrol(ctx, tenantID, log)
}

func (s *Service) runLivenessPatrol(ctx context.Context, tenantID string, log *slog.Logger) {
	t := time.NewTicker(livenessPatrolInterval)
	defer t.Stop()
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, livenessPatrolInterval)
		until, n, err := s.PatrolNodeLiveness(pctx, tenantID, since)
		cancel()
		switch {
		case err != nil:
			// 水位不前移：下一轮把这一段一起补上
			if ctx.Err() == nil {
				log.Warn("节点在线巡检失败", "error", err.Error())
			}
			continue
		case n > 0:
			log.Debug("节点在线状态翻转已通知", "count", n)
		}
		since = until
	}
}
