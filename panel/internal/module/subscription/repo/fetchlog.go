package repo

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// InsertFetchLog 追加一条拉取日志。
func (r *Repo) InsertFetchLog(ctx context.Context, l *model.FetchLog) error {
	return mapErr(r.d.Conn(ctx).Create(l).Error)
}

// PurgeFetchLogs 调库函数 purge_subscription_fetch_log(before, batch)，返回删除行数。
func (r *Repo) PurgeFetchLogs(ctx context.Context, before time.Time, batch int) (int64, error) {
	var n int64
	err := r.d.Conn(ctx).Raw("SELECT purge_subscription_fetch_log(?, ?)", before, batch).Scan(&n).Error
	return n, mapErr(err)
}
