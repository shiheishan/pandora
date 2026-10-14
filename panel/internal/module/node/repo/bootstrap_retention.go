package repo

import (
	"context"
	"time"
)

// PurgeBootstrapTokens 删一批没用过、且过期或作废早于 before 的安装令牌（最多 batch 行），返回删除行数。
//
// 用过的令牌跟身份走，永远不删（server_identities.enroll_token_id 以 RESTRICT 引用它）；被身份引用的
// 未用令牌按理不存在，这里也一并排除，免得外键报错让整批失败。库里的 bootstrap_tokens_guard（00037）
// 按库时钟再查一遍同一条件：Go 侧截止时刻若比库里的「now() - 30 天」还晚（时钟偏差），触发器拒绝、本批报错，不会多删。
// 表很小（令牌靠管理员手动签发），不另建索引。
func (r *Repo) PurgeBootstrapTokens(ctx context.Context, before time.Time, batch int) (int64, error) {
	res := r.d.Conn(ctx).Exec(`
		DELETE FROM bootstrap_tokens
		 WHERE id IN (
		       SELECT t.id
		         FROM bootstrap_tokens t
		        WHERE t.used_at IS NULL
		          AND (t.expires_at < ? OR t.voided_at < ?)
		          AND NOT EXISTS (SELECT 1 FROM server_identities i WHERE i.enroll_token_id = t.id)
		        ORDER BY t.expires_at
		        LIMIT ?)`,
		before, before, batch)
	return res.RowsAffected, res.Error
}
