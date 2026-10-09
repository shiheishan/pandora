package nodefabric

import (
	"context"
	"log/slog"
	"time"
)

// 下发变化的及时推送（用户 2026-10-08 定：订阅变化后节点 1–2 秒内收到）。
//
// 原先只有两路：Valkey 上的 node.users.changed（只有付款履约、后台加时长与流量、
// R104、门户换链接这几处写路径发），以及节点每 15 秒（AEGIS_NODE_PULL_INTERVAL）拉一次
// 名单。到期、流量用完、过期扫描、后台改账号状态、风控停用、运维直接执行的 SQL 都不发
// 信号，节点最多晚一个拉取周期（node-e2e 实测 0.27–14.8 秒）。
//
// 现在 aegis-node 自己盯下发纪元（00101 的 node_delivery_epoch 序列）：凡影响下发结果的
// 写一提交就推进它（触发器在库里，覆盖一切写入来源），每个有连接的租户每
// nodeEpochPollInterval 读一次（一条单行查询、不碰任何表），前进了就排一轮按池重算与推送。
// 到期没有写：名单里最早的到期时刻由推送那一轮带回，到点再排一轮。
//
// 这里不靠 LISTEN/NOTIFY：纪元是电平信号，读一次就知道有没有变，丢不了；代价是每租户每秒
// 两次序列读。缓存新旧另有纪元监听（epoch_watch.go，00153 的通知）判，它断了也不影响这条推送。
//
// 合并与节流：信号只登记「要推一轮」，同一轮里同池节点只算一次名单（缓存按纪元判新旧），
// 每条连接按手上的版本推增量或跳过；两轮之间至少隔 nodeFanoutMinInterval。所以
// 1000 个节点、批量到期或批量开单时，每个租户每秒至多一轮推送，一轮的查询数见
// processStreamPushes。

const (
	// nodeEpochPollInterval 是读下发纪元的间隔。
	nodeEpochPollInterval = 500 * time.Millisecond
	// nodeEpochSettle 是看到纪元前进之后、开始重算之前的等待。纪元在提交时推进（延迟
	// 约束触发器），比数据可见早一点点：紧接着就重算，可能读到提交前的旧名单、又把它
	// 记成新纪元的结果。等一小会儿让提交落定；多数情况下这次变化早在上一个读周期里
	// 就提交完了，这点等待只占 1–2 秒预算的一小部分。
	nodeEpochSettle = 200 * time.Millisecond
	// nodeEpochReadTimeout 是单次读纪元的上限，库慢时不拖住信号循环。
	nodeEpochReadTimeout = 2 * time.Second
)

// epochPoller 记着一个租户上一次读到的纪元。只在 WatchNodeChanges 的循环里用，不并发。
type epochPoller struct {
	s        *Service
	tenantID string
	last     int64
	known    bool
	failing  bool
}

// newEpochPoller 在有库的服务上返回轮询器；测试里没有库的服务返回 nil（只走 Valkey 那一路）。
func (s *Service) newEpochPoller(tenantID string) *epochPoller {
	if s.pool == nil {
		return nil
	}
	return &epochPoller{s: s, tenantID: tenantID}
}

// poll 读一次纪元。changed：纪元比上次读到的新（第一次读也算：这个租户的第一条连接
// 首推名单与这次读之间若有提交，不能漏掉）。expired：上一轮推送带回的最早到期时刻
// 已经到了。读失败只记一次日志，等下一个周期；节点的轮询兜底。
func (p *epochPoller) poll(ctx context.Context, q *streamPushQueue, now time.Time, log *slog.Logger) (changed, expired bool) {
	expired = q.takeExpiryDue(now)
	readCtx, cancel := context.WithTimeout(ctx, nodeEpochReadTimeout)
	epoch, err := p.s.CurrentDeliveryEpoch(readCtx, p.tenantID)
	cancel()
	if err != nil {
		if !p.failing && ctx.Err() == nil && log != nil {
			log.Warn("读下发纪元失败，下发变化暂靠节点轮询", "tenant_id", p.tenantID, "err", err)
		}
		p.failing = true
		return false, expired
	}
	p.failing = false
	if !p.known || epoch != p.last {
		p.last, p.known = epoch, true
		return true, expired
	}
	return false, expired
}
