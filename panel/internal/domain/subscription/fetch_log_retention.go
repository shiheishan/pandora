package subscription

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// FetchLogRetentionDays 是订阅拉取日志的保留期（用户定：原始数据 31 天），也是
// app.purge_subscription_fetch_log 接受的下限（00131）。后台读拉取日志最远的是用户画像
// 的最近 50 条与 7 天来源数、链接的来源窗口；行为趋势读 00114 的按天汇总，不依赖它。
const FetchLogRetentionDays = 31

// fetchLogPurgeBatch 行一批、一次调用最多 fetchLogPurgeMaxBatches 批，积压由下一轮继续清。
const (
	fetchLogPurgeBatch      = 5000
	fetchLogPurgeMaxBatches = 40
)

// PurgeFetchLog 删除本租户超出保留期的订阅拉取日志，返回删除的行数。由 aegis-admin 的保留期
// 任务定时调用，幂等；每批一个短事务。
//
// 拉取日志是追加写表，运行角色不能直接删；删除只经 app.purge_subscription_fetch_log（00131：
// 定义者权限、只删当前租户、保留期不少于 31 天、每次最多一批）。写成包级函数而不是 Service
// 方法：admin 进程没有常驻的订阅 Service 实例，清理也用不到它的缓存与盐。
func PurgeFetchLog(ctx context.Context, pool *db.Pool, tenantID string) (int64, error) {
	var total int64
	for range fetchLogPurgeMaxBatches {
		var n int64
		err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT app.purge_subscription_fetch_log($1, $2)`,
				FetchLogRetentionDays, fetchLogPurgeBatch).Scan(&n)
		})
		total += n
		if err != nil || n < fetchLogPurgeBatch {
			return total, err
		}
	}
	return total, nil
}
