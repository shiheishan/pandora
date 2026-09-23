package identity

// 邀请码。
//
// 只做「谁邀请了谁」这一层绑定，不在这里发奖励。
// 奖励涉及佣金、结算、风控确认，是另一套账；把它压进注册流程里，
// 意味着一次发奖失败会让新用户注册不成功 —— 那是本末倒置。
// 绑定关系落库之后，奖励可以随时基于 referrals 补算。

import (
	"context"
	"crypto/rand"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// InviteSummary 是用户自己的邀请概况。
type boundInvite struct {
	ID       string
	Owner    *string
	Channel  *string
	Campaign *string
}

// lockBoundInvite revalidates only the invite identity persisted at registration start.
// PostgreSQL now() is the sole expiry clock and the row stays locked through commit.
func lockBoundInvite(ctx context.Context, tx pgx.Tx, tenantID, inviteID string) (*boundInvite, error) {
	if inviteID == "" {
		return nil, nil
	}
	var out boundInvite
	err := tx.QueryRow(ctx, `
		SELECT id::text, owner_user_id::text, channel, campaign
		  FROM invite_codes
		 WHERE tenant_id = $1 AND id = $2::uuid
		   AND status = 'active'
		   AND (expires_at IS NULL OR expires_at > now())
		   AND (max_uses IS NULL OR used_count < max_uses)
		 FOR UPDATE`, tenantID, inviteID).Scan(
		&out.ID, &out.Owner, &out.Channel, &out.Campaign)
	if err == pgx.ErrNoRows {
		return nil, ErrRegistrationUnavailable
	}
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func consumeBoundInvite(
	ctx context.Context, tx pgx.Tx, tenantID, newUserID string, invite *boundInvite,
) error {
	if invite == nil {
		return nil
	}
	if invite.Owner != nil {
		if *invite.Owner == newUserID {
			return ErrRegistrationUnavailable
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO referrals
				(tenant_id, referee_user_id, referrer_user_id, invite_code_id, channel, campaign)
			VALUES ($1,$2::uuid,$3::uuid,$4::uuid,$5,$6)`,
			tenantID, newUserID, *invite.Owner, invite.ID,
			invite.Channel, invite.Campaign); err != nil {
			return err
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE invite_codes
		   SET used_count = used_count + 1,
		       status = CASE WHEN max_uses IS NOT NULL AND used_count + 1 >= max_uses
		                     THEN 'exhausted' ELSE status END
		 WHERE tenant_id = $1 AND id = $2::uuid
		   AND status = 'active'
		   AND (expires_at IS NULL OR expires_at > now())
		   AND (max_uses IS NULL OR used_count < max_uses)`, tenantID, invite.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrRegistrationUnavailable
	}
	return nil
}

type InviteSummary struct {
	Code      string `json:"code"`
	Invited   int    `json:"invited"`
	MaxUses   *int   `json:"max_uses"`
	CreatedAt any    `json:"created_at"`
}

// MyInviteCode 返回用户的邀请码，没有就生成一个。
//
// 懒生成而不是注册时就发：绝大多数用户从不使用邀请功能，
// 提前给每个人建一条记录只是在表里堆没人看的数据。
func (s *Service) MyInviteCode(ctx context.Context, tenantID, userID string) (*InviteSummary, error) {
	var out InviteSummary

	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT code, used_count, max_uses, created_at
			  FROM invite_codes
			 WHERE tenant_id = $1 AND owner_user_id = $2::uuid AND status = 'active'
			 ORDER BY created_at LIMIT 1`, tenantID, userID).Scan(
			&out.Code, &out.Invited, &out.MaxUses, &out.CreatedAt)
		if err == nil {
			return nil
		}
		if err != pgx.ErrNoRows {
			return err
		}

		// 生成一个短码。用户要口头念给朋友、发在聊天里，
		// 32 字节的随机串没人愿意抄 —— 8 位在几万用户规模下碰撞概率足够低，
		// 真撞上了唯一约束会挡住，下面重试即可。
		for attempt := 0; attempt < 5; attempt++ {
			code, err := newInviteCode()
			if err != nil {
				return err
			}
			var created any
			err = tx.QueryRow(ctx, `
				INSERT INTO invite_codes (tenant_id, code, owner_user_id, status)
				VALUES ($1,$2,$3::uuid,'active')
				ON CONFLICT DO NOTHING
				RETURNING created_at`, tenantID, code, userID).Scan(&created)
			if err == pgx.ErrNoRows {
				// 可能是短码碰撞，也可能是并发请求已经为 owner 建好了活动码。
				// 后一种必须返回赢家，而不是继续随机插入；00066 的部分唯一索引
				// 是最终并发防线。
				err = tx.QueryRow(ctx, `
					SELECT code, used_count, max_uses, created_at
					  FROM invite_codes
					 WHERE tenant_id=$1 AND owner_user_id=$2::uuid AND status='active'
					 LIMIT 1`, tenantID, userID).Scan(
					&out.Code, &out.Invited, &out.MaxUses, &out.CreatedAt)
				if err == nil {
					return nil
				}
				if err != pgx.ErrNoRows {
					return err
				}
				continue // 只是撞码了，换一个
			}
			if err != nil {
				return err
			}
			out = InviteSummary{Code: code, Invited: 0, CreatedAt: created}
			return nil
		}
		return httpx.Internal(nil)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// ListInvitees 返回该用户邀请来的人。
func (s *Service) ListInvitees(ctx context.Context, tenantID, userID string) ([]map[string]any, error) {
	out := []map[string]any{}
	err := s.pool.InTx(ctx, dbScope(tenantID, userID), func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT COALESCE(u.email,''), r.bound_at, r.risk_flag
			  FROM referrals r
			  JOIN users u ON u.id = r.referee_user_id
			 WHERE r.tenant_id = $1 AND r.referrer_user_id = $2::uuid
			 ORDER BY r.bound_at DESC LIMIT 100`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var email, risk string
			var at any
			if err := rows.Scan(&email, &at, &risk); err != nil {
				return err
			}
			// 邮箱打码：邀请人有权知道「谁通过我注册了」，
			// 但没必要拿到对方完整的联系方式
			out = append(out, map[string]any{
				"email": maskEmail(email), "bound_at": at, "risk_flag": risk,
			})
		}
		return rows.Err()
	})
	return out, err
}

func maskEmail(e string) string {
	at := strings.IndexByte(e, '@')
	if at <= 1 {
		return "***"
	}
	name := e[:at]
	if len(name) <= 2 {
		return name[:1] + "***" + e[at:]
	}
	return name[:2] + "***" + e[at:]
}

// newInviteCode 生成一个便于口头传播的短码。
//
// 去掉了 0/O/1/I/L 这几个容易看错的字符：邀请码常常是截图或口述传播的，
// 一个认错的字符就是一次失败的邀请，而用户不会想到是自己抄错了。
func newInviteCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

func dbScope(tenantID, userID string) db.Scope {
	return db.Scope{TenantID: tenantID, ActorID: userID}
}
