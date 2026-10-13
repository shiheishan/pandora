package repo

import (
	"context"

	"gorm.io/gorm"
)

// dispatchLockKey 是派发单实例互斥的咨询锁名。
const dispatchLockKey = "plugin:dispatch"

// WithDispatchLock 抢派发互斥锁，抢到就执行 fn 并返回 true；别的实例正在派发时不等待，直接返回 false。
//
// 用事务级的 pg_try_advisory_xact_lock：锁跟着一个只持有它的事务走，fn 结束后提交即释放；
// 进程崩溃、ctx 取消断开连接时事务随之结束，锁不会泄漏。会话级的锁要在同一条连接上配对 unlock，
// 连接池里 unlock 一旦没执行到（例如超时取消），锁就会留在那条被归还的连接上。
// 这个事务不做任何写入、不分配事务号，开着也不会拖住 vacuum。
// fn 拿到的是原 ctx（不带这个事务），里面的读写照常走连接池，各自提交。
func (r *Repo) WithDispatchLock(ctx context.Context, fn func(ctx context.Context) error) (bool, error) {
	var got bool
	err := r.d.Conn(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Raw("SELECT pg_try_advisory_xact_lock(hashtextextended(?, 0))", dispatchLockKey).Scan(&got).Error; err != nil {
			return err
		}
		if !got {
			return nil
		}
		return fn(ctx)
	})
	return got, err
}
