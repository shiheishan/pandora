package adminops

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 没有业务写入的后台敏感动作，也要留痕（审计台账 2.3 第 5、6 条）：
//
//   - 往外真发一条的测试接口（插件钩子、SMTP、Telegram、模板）：收件地址或目标可以随便填，
//     等于一个能让后台账号往任意地方发信的口子；
//   - 解密明文来源 IP 的读取（用户画像、访问明细、IP 聚类）：明文 IP 是个人信息。
//
// 这些动作在动作之前单独一个事务写审计：写不进去就不发、不返回明文（宁可失败也不留空白）。
// 不放进业务事务——它们根本没有业务事务。

// 测试发送的种类，写进审计摘要的 kind。
const (
	TestSendMailSettings = "mail_settings"
	TestSendMailTemplate = "mail_template"
	TestSendTelegram     = "telegram"
	TestSendPluginHook   = "plugin_hook"
)

// RecordTestSend 在发出测试消息之前记一条审计。target 是收件地址、chat id 或钩子代码；
// detail 是可选的补充（模板 code 等）。
func (s *Service) RecordTestSend(ctx context.Context, tenantID, actorID, kind, target string,
	detail map[string]any) error {
	digest := map[string]any{"kind": kind, "target": target}
	for k, v := range detail {
		digest[k] = v
	}
	return s.recordAdminAction(ctx, tenantID, actorID, audit.Entry{
		Action: "notify.test_sent", ResourceType: "notification_test", AfterDigest: digest,
	})
}

// 解密明文来源 IP 的读取视图，写进审计摘要的 view。
const (
	SourceIPViewUserProfile = "user_profile"
	SourceIPViewAccessLog   = "access_log"
	SourceIPViewIPClusters  = "ip_clusters"
)

// RecordSourceIPView 在把解密后的明文来源 IP 交给后台之前记一条审计。userID 非空时
// 挂在那个用户上（用户画像）；filter 是这次查看的条件（访问明细的筛选、聚类的翻页等）。
func (s *Service) RecordSourceIPView(ctx context.Context, tenantID, actorID, view, userID string,
	filter map[string]any) error {
	e := audit.Entry{
		Action:      "security.source_ip_viewed",
		AfterDigest: map[string]any{"view": view, "filter": filter},
	}
	if userID != "" {
		e.ResourceType, e.ResourceID = "user", &userID
	}
	return s.recordAdminAction(ctx, tenantID, actorID, e)
}

func (s *Service) recordAdminAction(ctx context.Context, tenantID, actorID string, e audit.Entry) error {
	e.ActorKind = "admin"
	if actorID != "" {
		e.ActorID = &actorID
	}
	e.APIDomain, e.RequestID = "admin", httpx.RequestIDFrom(ctx)
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, tenantID, e)
	})
}
