package identity

import (
	"context"
	"encoding/hex"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 登录失败的审计（审计台账 2.3 第 3 条：爆破管理员口令事后无据可查）。
//
// 每次失败都写一行，爆破时审计表会跟着暴涨，而且每行都要排同租户的审计链锁。所以按
// 来源 IP 与账号两个维度聚合限频：同一个 IP、或同一个邮箱，在 loginFailureAuditWindow
// 内已经记过一条失败，就不再记。单 IP 喷洒很多账号、或很多 IP 撞同一个账号，每个窗口
// 都只留一行；行里带邮箱哈希、来源 IP（Write 自己算哈希并加密）与失败原因，够事后追查。
//
// 主体记 anonymous、actor_id 留空：失败的登录还不是任何人。这也让它进不了 IP 聚类视图
// （那里只算 actor_kind='user' 且有 actor_id 的事件），不会把爆破者和被撞的账号算成同伙。
// 写在登录主事务之外、尽力而为：审计写不进去只记日志，不改变给调用方的响应。

const (
	loginFailureAuditAction = "user.login_failed"
	loginFailureAuditWindow = "10 minutes"
)

// 失败原因，写进 error_code。对外一律是同一句「邮箱或密码不正确」（禁登门户除外）。
const (
	loginFailureInvalid     = "invalid_credentials"
	loginFailureInactive    = "account_inactive"
	loginFailureNoAdminRole = "no_admin_role"
	loginFailureStaffPortal = "staff_portal_blocked"
)

// loginFailureSeenSQL 判断这个窗口里同一 IP 或同一邮箱是否已经记过失败。
// 走 idx_audit_events_action (tenant_id, action, occurred_at DESC)：窗口内的失败行
// 本身就被这里限着，扫到的行数很少。
const loginFailureSeenSQL = `
	SELECT EXISTS (
		SELECT 1 FROM audit_events
		 WHERE tenant_id = $1 AND action = '` + loginFailureAuditAction + `'
		   AND occurred_at > now() - interval '` + loginFailureAuditWindow + `'
		   AND ((cardinality($2::bytea[]) > 0 AND source_ip_hash = ANY($2::bytea[]))
		        OR after_digest->>'email_hash' = $3))`

type loginFailure struct {
	Email     string
	UserID    string // 账号存在时才有
	Audience  string
	Reason    string
	IP        string
	IPHash    []byte
	UserAgent string
}

// recordLoginFailure 按 IP 与账号聚合限频地记一条登录失败。
func (s *Service) recordLoginFailure(ctx context.Context, tenantID string, f loginFailure) {
	if s.pool == nil {
		return
	}
	emailHash := hex.EncodeToString(crypto.HashIdentifier(s.hashSalt, f.Email))
	ipHashes := [][]byte{}
	if len(f.IPHash) > 0 {
		ipHashes = append(ipHashes, f.IPHash)
	}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		// 「先查后写」用一把只管登录失败的事务锁串起来，两个并发的失败不会都以为
		// 窗口里还没有记录。它只挡登录失败的审计，不挡成功登录与业务事务
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
			"login_failed_audit:"+tenantID); err != nil {
			return err
		}
		var seen bool
		if err := tx.QueryRow(ctx, loginFailureSeenSQL, tenantID, ipHashes, emailHash).Scan(&seen); err != nil {
			return err
		}
		if seen {
			return nil
		}
		entry := audit.Entry{
			ActorKind: "anonymous", Action: loginFailureAuditAction,
			AfterDigest: map[string]any{"email_hash": emailHash, "reason": f.Reason},
			APIDomain:   f.Audience, Outcome: "failure", ErrorCode: f.Reason,
			RequestID: httpx.RequestIDFrom(ctx), SourceIP: f.IP, SourceIPHash: f.IPHash,
			UserAgent: f.UserAgent,
		}
		if f.UserID != "" {
			userID := f.UserID
			entry.ResourceType, entry.ResourceID = "user", &userID
		}
		return audit.Write(ctx, tx, tenantID, entry)
	})
	if err != nil {
		slog.Default().WarnContext(ctx, "登录失败审计写入失败", "error", err.Error(),
			"request_id", httpx.RequestIDFrom(ctx))
	}
}
