// [INPUT]: 依赖 00026 的 audit_ip_clusters 视图、00077 的 ip_cluster_reviews，依赖同包 service.go 的 revokeUserLogins，依赖 platform 的 db/audit/httpx/iamguard
// [OUTPUT]: 对外提供 IPCluster、IPClusterUser、IPClusterReview、DisableClusterResult、ParseClusterKey、Service.ListIPClusters / ReviewIPCluster / DisableIPClusterAccounts
// [POS]: adminops 的风控聚类用例（后台-09「风控」卡片）：读聚类与复核结论、标记正常、批量停用；停用逐个复用改用户状态的语义，全部在一个事务里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/iamguard"
)

// 聚类是线索不是结论：学校、公司、家庭出口下多人共用 IP 很正常。所以这里
// 给的两个处置都是可逆的——「正常」30 天后过期重新提示，「停用」用
// suspended 而不是 banned，误伤了能在用户页恢复。

// IPClusterNormalTTL 是「标记为正常」的有效期。
const IPClusterNormalTTL = 30 * 24 * time.Hour

type IPClusterUser struct {
	ID         string  `json:"id"`
	Email      string  `json:"email"`
	Status     string  `json:"status"`
	ActivePlan *string `json:"active_plan"`
}

type IPClusterReview struct {
	Decision  string     `json:"decision"`
	DecidedAt time.Time  `json:"decided_at"`
	ExpiresAt *time.Time `json:"expires_at"`
}

// IPCluster 是一个共享来源 IP 的聚类。明文 IP 只以密文给出，由 api 层解密
// 并补归属地与风险等级——领域层不持有信封密钥。
type IPCluster struct {
	Key      string           `json:"key"`
	IPEnc    []byte           `json:"-"`
	Accounts int              `json:"accounts"`
	Events   int              `json:"events"`
	First    time.Time        `json:"first"`
	Last     time.Time        `json:"last"`
	Emails   []string         `json:"emails"`
	Users    []IPClusterUser  `json:"users"`
	Review   *IPClusterReview `json:"review"`
}

// ParseClusterKey 把接口上的 key（source_ip_hash 的 hex）还原成哈希。
// 格式不对与聚类不存在一样回 404：key 不是用户输入，是列表接口给出去的。
func ParseClusterKey(key string) ([]byte, error) {
	b, err := hex.DecodeString(strings.TrimSpace(key))
	if err != nil || len(b) == 0 {
		return nil, httpx.NotFoundOrForbidden()
	}
	return b, nil
}

// ListIPClusters 列出近 90 天关联到多个账号的来源 IP，账号多的在前，至多 50 个。
// 标记为正常且未过期的默认不列（includeReviewed 时列出）；已停用的照常列出，
// 带着结论，免得有人对同一批账号再处置一遍。
func (s *Service) ListIPClusters(ctx context.Context, tenantID string, includeReviewed bool) ([]IPCluster, error) {
	out := []IPCluster{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT encode(c.source_ip_hash, 'hex'), c.account_count, c.event_count,
			       c.first_seen, c.last_seen,
			       COALESCE((SELECT a.source_ip_enc FROM audit_events a
			                  WHERE a.tenant_id = c.tenant_id
			                    AND a.source_ip_hash = c.source_ip_hash
			                    AND a.source_ip_enc IS NOT NULL
			                  LIMIT 1), ''::bytea),
			       c.accounts, r.decision, r.decided_at, r.expires_at
			  FROM audit_ip_clusters c
			  LEFT JOIN ip_cluster_reviews r
			    ON r.tenant_id = c.tenant_id AND r.source_ip_hash = c.source_ip_hash
			 WHERE c.tenant_id = $1
			   AND ($2 OR r.decision IS DISTINCT FROM 'normal' OR r.expires_at <= now())
			 ORDER BY c.account_count DESC, c.last_seen DESC
			 LIMIT 50`, tenantID, includeReviewed)
		if err != nil {
			return err
		}
		members := map[int][]string{}
		for rows.Next() {
			var c IPCluster
			var ids []string
			var decision *string
			var decidedAt, expiresAt *time.Time
			if err := rows.Scan(&c.Key, &c.Accounts, &c.Events, &c.First, &c.Last, &c.IPEnc,
				&ids, &decision, &decidedAt, &expiresAt); err != nil {
				rows.Close()
				return err
			}
			if decision != nil && decidedAt != nil {
				c.Review = &IPClusterReview{Decision: *decision, DecidedAt: *decidedAt, ExpiresAt: expiresAt}
			}
			members[len(out)] = ids
			out = append(out, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		// 成员资料一次取齐再分发，而不是每个聚类查一遍
		var all []string
		for _, ids := range members {
			all = append(all, ids...)
		}
		users := map[string]IPClusterUser{}
		urows, err := tx.Query(ctx, `
			SELECT u.id::text, u.email::text, u.status::text,
			       (SELECT pl.name FROM subscriptions s
			          JOIN plans pl ON pl.id = s.plan_id
			         WHERE s.user_id = u.id AND s.status IN ('active','trialing')
			         ORDER BY s.created_at DESC LIMIT 1)
			  FROM users u
			 WHERE u.tenant_id = $1 AND u.id = ANY($2::uuid[])`, tenantID, all)
		if err != nil {
			return err
		}
		defer urows.Close()
		for urows.Next() {
			var u IPClusterUser
			if err := urows.Scan(&u.ID, &u.Email, &u.Status, &u.ActivePlan); err != nil {
				return err
			}
			users[u.ID] = u
		}
		if err := urows.Err(); err != nil {
			return err
		}
		for i := range out {
			out[i].Users = []IPClusterUser{}
			out[i].Emails = []string{}
			for _, id := range members[i] {
				if u, ok := users[id]; ok {
					out[i].Users = append(out[i].Users, u)
					out[i].Emails = append(out[i].Emails, u.Email)
				}
			}
			slices.SortFunc(out[i].Users, func(a, b IPClusterUser) int { return strings.Compare(a.Email, b.Email) })
			slices.Sort(out[i].Emails)
		}
		return nil
	})
	if err != nil {
		return nil, httpx.Internal(err)
	}
	return out, nil
}

// clusterMembers 锁定一个聚类的当前成员。视图按 90 天窗口现算，
// 聚类不存在（key 错、窗口外、只剩一个账号）即 404。
func clusterMembers(ctx context.Context, tx pgx.Tx, tenantID string, hash []byte) ([]string, error) {
	var ids []string
	err := tx.QueryRow(ctx, `
		SELECT accounts FROM audit_ip_clusters
		 WHERE tenant_id = $1 AND source_ip_hash = $2`, tenantID, hash).Scan(&ids)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, httpx.NotFoundOrForbidden()
	}
	return ids, err
}

// upsertClusterReview 写下一个聚类的当前结论（一个聚类只留一行），返回行 id。
func upsertClusterReview(ctx context.Context, tx pgx.Tx, tenantID string, hash []byte,
	decision, note, actorID string, expiresAt *time.Time) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO ip_cluster_reviews
		  (tenant_id, source_ip_hash, decision, note, decided_by, decided_at, expires_at)
		VALUES ($1, $2, $3, $4, $5::uuid, now(), $6)
		ON CONFLICT (tenant_id, source_ip_hash) DO UPDATE
		   SET decision = EXCLUDED.decision, note = EXCLUDED.note,
		       decided_by = EXCLUDED.decided_by, decided_at = EXCLUDED.decided_at,
		       expires_at = EXCLUDED.expires_at
		RETURNING id::text`, tenantID, hash, decision, note, actorID, expiresAt).Scan(&id)
	return id, err
}

// ReviewIPCluster 把聚类标记为正常，30 天内不再出现在默认列表里。
// 重复标记只刷新有效期与备注。
func (s *Service) ReviewIPCluster(ctx context.Context, tenantID, actorID, key, note string) (time.Time, error) {
	hash, err := ParseClusterKey(key)
	if err != nil {
		return time.Time{}, err
	}
	note = strings.TrimSpace(note)
	if utf8.RuneCountInString(note) > 500 {
		return time.Time{}, httpx.Invalid(map[string]string{"note": "备注不能超过 500 字"})
	}
	expires := time.Now().Add(IPClusterNormalTTL).UTC().Truncate(time.Microsecond)
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if _, err := clusterMembers(ctx, tx, tenantID, hash); err != nil {
			return err
		}
		id, err := upsertClusterReview(ctx, tx, tenantID, hash, "normal", note, actorID, &expires)
		if err != nil {
			return err
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actorID, Action: "risk.ip_cluster.mark_normal",
			ResourceType: "ip_cluster_review", ResourceID: &id, APIDomain: "admin",
			Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"key": key, "note": note, "expires_at": expires},
		})
	})
	if err != nil {
		return time.Time{}, riskError(err)
	}
	return expires, nil
}

// 批量停用时逐个账号的跳过原因。
const (
	SkipSelf            = "self"
	SkipAdministrator   = "administrator"
	SkipNotMember       = "not_member"
	SkipAlreadyDisabled = "already_disabled"
)

type ClusterSkip struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
}

type DisableClusterResult struct {
	Disabled int           `json:"disabled"`
	Skipped  []ClusterSkip `json:"skipped"`
}

// normalizeDisableInput 校验批量停用的请求：原因 5–500 字，账号 id 非空、
// 都是 UUID、至多 200 个（一个聚类最多也就展示这么多），去重并排序——
// 固定的加锁顺序让两个并发的批量停用不会互相死锁。
func normalizeDisableInput(userIDs []string, reason string) ([]string, string, error) {
	reason = strings.TrimSpace(reason)
	fields := map[string]string{}
	if n := utf8.RuneCountInString(reason); n < 5 || n > 500 {
		fields["reason"] = "原因必须为 5 到 500 字"
	}
	ids := make([]string, 0, len(userIDs))
	for _, raw := range userIDs {
		id, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			fields["user_ids"] = "包含无效的账号 ID"
			break
		}
		ids = append(ids, id.String())
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if fields["user_ids"] == "" && (len(ids) == 0 || len(ids) > 200) {
		fields["user_ids"] = "请选择 1 到 200 个账号"
	}
	if len(fields) > 0 {
		return nil, "", httpx.Invalid(fields)
	}
	return ids, reason, nil
}

// DisableIPClusterAccounts 停用聚类里选中的账号（status=suspended）。
//
// 逐个复用改用户状态的语义：吊销会话与 refresh 令牌、每人一条
// user.status_change 审计、不能停自己、事务末尾核对仍有有效管理员。
// 另外跳过持有任何后台角色的账号——员工账号出现在聚类里多半是在公司网络下
// 登录过门户，停用它应当走用户管理页、看清角色之后再做，而不是被一键带走。
// 全部在一个事务里：要么这一批都按结论处置，要么都不动。
func (s *Service) DisableIPClusterAccounts(ctx context.Context, tenantID, actorID, key string, userIDs []string, reason string) (*DisableClusterResult, error) {
	hash, err := ParseClusterKey(key)
	if err != nil {
		return nil, err
	}
	ids, reason, err := normalizeDisableInput(userIDs, reason)
	if err != nil {
		return nil, err
	}
	out := &DisableClusterResult{Skipped: []ClusterSkip{}}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		if err := iamguard.LockLastAdministrator(ctx, tx, tenantID); err != nil {
			return err
		}
		members, err := clusterMembers(ctx, tx, tenantID, hash)
		if err != nil {
			return err
		}
		skip := func(id, why string) { out.Skipped = append(out.Skipped, ClusterSkip{UserID: id, Reason: why}) }
		var disabled []string
		for _, id := range ids {
			if id == actorID {
				skip(id, SkipSelf)
				continue
			}
			if !slices.Contains(members, id) {
				skip(id, SkipNotMember)
				continue
			}
			var before string
			var staff bool
			err := tx.QueryRow(ctx, `
				SELECT u.status::text,
				       EXISTS (SELECT 1 FROM role_bindings rb
				                WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id)
				  FROM users u WHERE u.tenant_id = $1 AND u.id = $2::uuid
				   FOR UPDATE OF u`, tenantID, id).Scan(&before, &staff)
			if errors.Is(err, pgx.ErrNoRows) {
				skip(id, SkipNotMember)
				continue
			}
			if err != nil {
				return err
			}
			if staff {
				skip(id, SkipAdministrator)
				continue
			}
			if before == "suspended" || before == "banned" {
				skip(id, SkipAlreadyDisabled)
				continue
			}
			if _, err := tx.Exec(ctx,
				`UPDATE users SET status = 'suspended' WHERE tenant_id = $1 AND id = $2::uuid`,
				tenantID, id); err != nil {
				return err
			}
			revoked, err := revokeUserLogins(ctx, tx, tenantID, id, "suspended")
			if err != nil {
				return err
			}
			userID := id
			if err := audit.Write(ctx, tx, tenantID, audit.Entry{
				ActorKind: "admin", ActorID: &actorID,
				Action: "user.status_change", ResourceType: "user", ResourceID: &userID,
				APIDomain: "admin", Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
				BeforeDigest: map[string]any{"status": before},
				AfterDigest: map[string]any{"status": "suspended", "reason": reason,
					"sessions_revoked": revoked, "source": "ip_cluster", "key": key},
			}); err != nil {
				return err
			}
			disabled = append(disabled, id)
		}
		if err := iamguard.RequireEffectiveAdministrator(ctx, tx, tenantID); err != nil {
			return err
		}
		out.Disabled = len(disabled)

		// 一个都没停成就不改结论：聚类仍然待处置，不该从列表上消失成「已停用」
		var reviewID *string
		if len(disabled) > 0 {
			id, err := upsertClusterReview(ctx, tx, tenantID, hash, "disabled", reason, actorID, nil)
			if err != nil {
				return err
			}
			reviewID = &id
		}
		entry := audit.Entry{
			ActorKind: "admin", ActorID: &actorID, Action: "risk.ip_cluster.disable",
			ResourceID: reviewID, APIDomain: "admin", Outcome: "success",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"key": key, "reason": reason,
				"disabled": disabled, "skipped": out.Skipped},
		}
		if reviewID != nil {
			entry.ResourceType = "ip_cluster_review"
		}
		if len(disabled) == 0 {
			entry.Outcome = "failure"
		} else if len(out.Skipped) > 0 {
			entry.Outcome = "partial"
		}
		return audit.Write(ctx, tx, tenantID, entry)
	})
	if err != nil {
		return nil, riskError(err)
	}
	return out, nil
}

func riskError(err error) error {
	if httpErr := new(httpx.Error); errors.As(err, &httpErr) {
		return err
	}
	return httpx.Internal(err)
}
