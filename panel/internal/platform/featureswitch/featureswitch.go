// Package featureswitch 读降级开关（feature_switches）的一行，供中间件的开关门使用。
//
// SQL 放在 platform：中间件不写 SQL、也不 import domain（开关的写入方是 adminops）。
// 进程内缓存与失效在 middleware/switches.go（platform/cache 的 Cache）。
package featureswitch

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// readSQL 取一个开关的当前值。
const readSQL = `SELECT enabled FROM feature_switches WHERE tenant_id = $1 AND code = $2`

// Querier 是本包对数据库的全部要求（*db.Pool 的 QueryRowScoped，一次往返）。
type Querier interface {
	QueryRowScoped(ctx context.Context, s db.Scope, sql string, args []any, dest ...any) error
}

// Enabled 读开关 code 是否开启。缺行视为开启（fail open）：这几个开关是运维手里的「急停」，
// 迁移只给当时已有的租户插了行，之后新建的租户没有行；把缺行当关闭，新租户会一上来就不能下单。
// auth.registration 是反例（缺行即关闭），那是注册策略自己的规则，不走这里。读库出错原样返回。
func Enabled(ctx context.Context, q Querier, tenantID, code string) (bool, error) {
	enabled := true
	err := q.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, readSQL, []any{tenantID, code}, &enabled)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return enabled, nil
}
