package identity

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

// 管理员替用户重置密码。
//
// 在这之前，用户忘了密码就永久失联：找回密码没有实现，改密码要求填旧
// 密码，管理员也没有任何办法插手，而这台机器的 SMTP 出站被封、邮件根本
// 发不出去。对付费用户这是灾难——换台设备想不起密码，只能退款。
//
// xboard 的做法很直接：后台编辑用户时有个 password 字段，管理员填了就
// 改掉。用户联系客服，客服改完把新密码告诉他。这不是最好的体验（更好的
// 是用户自助邮件重置），但它不依赖任何外部服务，今天就能用。
//
// 安全上和用户自己改密码同等对待：
//
//   - 旧会话全部吊销。改密的前提往往是「凭据可能已经泄露」，留着旧会话
//     等于让攻击者继续驻留。
//   - 刷新令牌与令牌家族一并作废，否则能绕过会话吊销换新的访问令牌。
//   - 写审计，记下是哪个管理员改的、为什么改。用户事后说「我没改过密码」
//     时，这是唯一能对上的线索。
//
// 刻意不做的一件事：不返回、不记录新密码。管理员是自己填的，本来就知道；
// 而把它写进响应体，就会顺着日志、浏览器历史、截图流出去。

type AdminResetPasswordInput struct {
	// TargetUserID 是被改密码的用户。
	TargetUserID string
	// ActorID 是执行这次操作的管理员。
	ActorID     string
	NewPassword string
	// Reason 可选（R101）：给了就写进审计，空串不写。
	Reason    string
	APIDomain string
	IP        string
	UserAgent string
}

// AdminResetPassword 由管理员直接设置某个用户的新密码。
//
// 越级检查（iamguard.CanManage）：目标账号持有操作者没有的权限时拒绝（403）。
// 没有它，持 iam.user.write 的租户管理员能重置平台管理员的密码、再用新密码登录，
// 一步提权到全部权限。口令策略按目标账号算：目标是后台人员时至少 12 位。
func (s *Service) AdminResetPassword(ctx context.Context, tenantID string,
	in AdminResetPasswordInput) error {

	if tenantID == "" || in.TargetUserID == "" || in.ActorID == "" {
		return httpx.New(httpx.CodeBadRequest, "缺少租户、管理员或目标用户")
	}
	if in.TargetUserID == in.ActorID {
		// 改自己的密码要走 /me/password，那条路要求填旧密码。
		// 从这里绕过去，等于给「拿到会话就能改自己密码」开了口子。
		return httpx.New(httpx.CodeBadRequest,
			"改自己的密码请用「修改密码」，那里会先验证当前密码")
	}
	if err := validatePassword(in.NewPassword); err != nil {
		return err
	}

	target := in.TargetUserID
	actor := in.ActorID

	// 名额在开事务之前拿：不拿着连接、锁着目标行排队等哈希
	slot, err := acquirePasswordSlot(ctx)
	if err != nil {
		return err
	}
	defer slot.Release()

	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor},
		func(tx pgx.Tx) error {
			// 锁住目标行：与并发的改状态（FOR UPDATE）、重置互斥，越级检查与写入针对
			// 同一个账号状态。用 NO KEY UPDATE：不挡引用这行的外键检查（如目标用户
			// 此刻的登录建会话），只挡同样要改这行的事务
			var email string
			err := tx.QueryRow(ctx, `
				SELECT email::text FROM users
				 WHERE tenant_id = $1 AND id = $2::uuid
				 FOR NO KEY UPDATE`,
				tenantID, target).Scan(&email)
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			if err != nil {
				return err
			}

			authority, err := iamguard.CanManage(ctx, tx, tenantID, actor, target)
			if err != nil {
				return err
			}
			if err := validatePasswordFor(authority.Staff, in.NewPassword); err != nil {
				return err
			}

			newPHC, err := slot.Hash(in.NewPassword, crypto.DefaultArgon2Params())
			if err != nil {
				return err
			}

			// 用户可能从来没设过密码（比如只用过快捷登录），所以是 upsert
			// 而不是 update —— 那种账号恰恰最需要管理员能帮他设一个。
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_passwords (tenant_id, user_id, phc, rotated_at, must_rotate)
				VALUES ($1, $2::uuid, $3, now(), false)
				ON CONFLICT (user_id) DO UPDATE
				   SET phc = EXCLUDED.phc, rotated_at = now(), must_rotate = false`,
				tenantID, target, newPHC); err != nil {
				return err
			}

			// 会话、刷新令牌、令牌家族一起吊销（与自助找回密码同一份实现）。
			// 少任何一步，旧凭据都还能用。
			if err := revokeAllLogins(ctx, tx, tenantID, target, "password_reset_by_admin"); err != nil {
				return err
			}

			return audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind:    "admin",
				ActorID:      &actor,
				Action:       "user.password_reset_by_admin",
				ResourceType: "user",
				ResourceID:   &target,
				APIDomain:    in.APIDomain,
				Outcome:      "success",
				RequestID:    httpx.RequestIDFrom(ctx),
				SourceIP:     in.IP,
				UserAgent:    in.UserAgent,
				AfterDigest:  resetAuditDigest(email, in.Reason),
			})
		})
}

// resetAuditDigest 是改密审计的摘要：原因只在给了时出现，不写空串占位。
func resetAuditDigest(email, reason string) map[string]any {
	out := map[string]any{"target_email": email, "sessions_revoked": true}
	if reason != "" {
		out["reason"] = reason
	}
	return out
}
