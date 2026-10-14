package db

// 每请求 SQL 计数（reqstats）：GORM 回调按 ctx 里的计数器记条数与耗时。
// 事务里的语句经 DB.Conn(ctx) 带着同一个 ctx，所以也算；
// BEGIN / COMMIT 不经这些回调，不计；
// Rows 流式读取的时间只算到回调结束。

import (
	"errors"
	"time"

	"github.com/aegispanel/aegis/internal/pkg/reqstats"
	"gorm.io/gorm"
)

const statsStartKey = "pandora:reqstats_start"

// registerStatsCallbacks 为 GORM 注册 SQL 执行计数与耗时累加回调。
func registerStatsCallbacks(g *gorm.DB) error {
	var errs []error
	cb := g.Callback()

	add := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	add(cb.Create().Before("gorm:create").Register("pandora:reqstats_before_create", statsBefore))
	add(cb.Create().After("gorm:create").Register("pandora:reqstats_after_create", statsAfter))

	add(cb.Query().Before("gorm:query").Register("pandora:reqstats_before_query", statsBefore))
	add(cb.Query().After("gorm:query").Register("pandora:reqstats_after_query", statsAfter))

	add(cb.Update().Before("gorm:update").Register("pandora:reqstats_before_update", statsBefore))
	add(cb.Update().After("gorm:update").Register("pandora:reqstats_after_update", statsAfter))

	add(cb.Delete().Before("gorm:delete").Register("pandora:reqstats_before_delete", statsBefore))
	add(cb.Delete().After("gorm:delete").Register("pandora:reqstats_after_delete", statsAfter))

	add(cb.Row().Before("gorm:row").Register("pandora:reqstats_before_row", statsBefore))
	add(cb.Row().After("gorm:row").Register("pandora:reqstats_after_row", statsAfter))

	add(cb.Raw().Before("gorm:raw").Register("pandora:reqstats_before_raw", statsBefore))
	add(cb.Raw().After("gorm:raw").Register("pandora:reqstats_after_raw", statsAfter))

	return errors.Join(errs...)
}

func statsBefore(tx *gorm.DB) {
	if tx == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	if reqstats.From(tx.Statement.Context) == nil {
		return
	}
	tx.InstanceSet(statsStartKey, time.Now())
}

func statsAfter(tx *gorm.DB) {
	if tx == nil || tx.Statement == nil || tx.Statement.Context == nil {
		return
	}
	st := reqstats.From(tx.Statement.Context)
	if st == nil {
		return
	}
	if tx.Statement.SQL.Len() == 0 {
		return
	}
	if val, ok := tx.InstanceGet(statsStartKey); ok {
		if start, ok := val.(time.Time); ok {
			st.AddSQL(time.Since(start))
			return
		}
	}
	st.AddSQL(0)
}
