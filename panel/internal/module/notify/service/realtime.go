package service

// 公告的实时推送：announcements 的新建、编辑、发布、撤回（含定时任务把到点公告转发布）推 announcements.changed
// 给门户公共频道与管理端，门户公告栏与后台公告页据此重拉（定向公告由读接口按人过滤，推送只说「变了」）。
// 在写成功的事务里调，提交后才真正发出（realtime.Publisher 内部用 tx.AfterCommit），回滚不发。

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// rtTableAnnouncements 是推送载荷里的表名。
const rtTableAnnouncements = "announcements"

// UseRealtime 接上实时推送（启动装配时调用，不并发安全）。没接上时不推。
func (s *Service) UseRealtime(p *realtime.Publisher) { s.rt = p }

// announcementChanged 在当前事务提交后推一条公告行变更。op 取 realtime.Op*。
func (s *Service) announcementChanged(ctx context.Context, op string, id uuid.UUID) {
	s.rt.Changed(ctx, realtime.TopicAnnouncements, rtTableAnnouncements, op, id, nil)
}
