// Package tx 是 service 开事务的唯一入口。service 只依赖本包，不直接接触 GORM。
package tx

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"gorm.io/gorm"
)

// Manager 开启事务，并在提交后执行登记的函数。
type Manager struct {
	db *db.DB
}

func NewManager(d *db.DB) *Manager { return &Manager{db: d} }

// Do 在事务里执行 fn。ctx 里已有事务时直接加入，不再嵌套开启；
// 只有最外层的 Do 提交，提交成功后按登记顺序执行 AfterCommit 的函数。
func (m *Manager) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	if db.InTx(ctx) {
		return fn(ctx)
	}
	var st *db.TxState
	err := m.db.Base().WithContext(ctx).Transaction(func(t *gorm.DB) error {
		st = db.NewTxState(t)
		return fn(db.WithTxState(ctx, st))
	})
	if err != nil {
		return err
	}
	after := context.WithoutCancel(ctx)
	for _, h := range st.Hooks() {
		h(after)
	}
	return nil
}

// AfterCommit 登记事务提交后执行的函数；回滚时丢弃。key 非空时同一事务内按 key 去重。
// ctx 里没有事务时立即执行（数据已经落库）。
func AfterCommit(ctx context.Context, key string, fn func(ctx context.Context)) {
	if !db.AddHook(ctx, key, fn) {
		fn(context.WithoutCancel(ctx))
	}
}
