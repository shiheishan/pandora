package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/integrity"
)

// RedemptionMismatchRows 找码与兑换流水对不上的卡：标了 used 却没有兑换流水，或有兑换流水而码不是 used、兑换人不是 used_by。
// 前半走 gift_card_codes_used_by_idx（used_by 非空的部分索引，used_by 非空等价于 used，库里 CHECK 钉住）+ 兑换流水 code_id 唯一索引反连接；
// 后半扫兑换流水（行数等于兑换次数）按主键连码。样例是码 id。
func (r *Repo) RedemptionMismatchRows(ctx context.Context) ([]integrity.Row, error) {
	var out []integrity.Row
	err := r.d.Conn(ctx).Raw(`
		SELECT x.id::text AS id, count(*) OVER () AS n
		  FROM (
		        SELECT c.id
		          FROM gift_card_codes c
		         WHERE c.used_by IS NOT NULL
		           AND NOT EXISTS (SELECT 1 FROM gift_card_redemptions r WHERE r.code_id = c.id)
		        UNION
		        SELECT c.id
		          FROM gift_card_redemptions r
		          JOIN gift_card_codes c ON c.id = r.code_id
		         WHERE c.status <> 'used' OR c.used_by IS DISTINCT FROM r.user_id) x
		 ORDER BY x.id DESC
		 LIMIT ?`, integrity.SampleLimit).Scan(&out).Error
	if err != nil {
		return nil, mapErr(err)
	}
	return out, nil
}
