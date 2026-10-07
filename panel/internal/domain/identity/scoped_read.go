package identity

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// scopedReader 是 *db.Pool 的单往返读法（注入租户与语句同批，不开 BEGIN / COMMIT）。
//
// Service.pool 只声明了 InTx（单测的假实现只实现它），所以读路径按需断言：真连接池走
// 一次往返，假实现退回 InTx，行为相同。
type scopedReader interface {
	BatchScoped(ctx context.Context, s db.Scope, opts db.BatchOptions, b *pgx.Batch) error
}

// readBatch 把只读批次在一次往返内发出；pool 不支持时退回 InTx 逐条执行。
// 批次里的语句互不依赖，回调只读结果；任一条出错（含 QueryRow 查无此行）整批返回该错误。
func (s *Service) readBatch(ctx context.Context, scope db.Scope, b *pgx.Batch) error {
	if r, ok := s.pool.(scopedReader); ok {
		return r.BatchScoped(ctx, scope, db.BatchOptions{}, b)
	}
	return s.pool.InTx(ctx, scope, func(tx pgx.Tx) error {
		return tx.SendBatch(ctx, b).Close()
	})
}
