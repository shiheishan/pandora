package seed

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

// planTerms 是订阅要快照的套餐条款：已发布版本与在售价格。
type planTerms struct {
	PlanID    string
	VersionID string
	PriceID   string
	Currency  string
	Amount    int64
	// PeriodStart / PeriodEnd 全批共用：一个月的计费周期，与价格的 month × 1 一致
	PeriodStart time.Time
	PeriodEnd   time.Time
}

// loadPlanTerms 读刚发布的套餐：当前版本必须就是向导建的那个版本，价格取最早一条在售价（与礼品卡开通同一挑法）。
func loadPlanTerms(ctx context.Context, pool *db.Pool, tenantID, planID, versionID string, now time.Time) (*planTerms, error) {
	t := &planTerms{PlanID: planID, PeriodStart: now, PeriodEnd: now.AddDate(0, 1, 0)}
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT coalesce(p.current_version_id::text,''), pr.id::text, pr.currency::text, pr.unit_amount
			  FROM plans p
			  JOIN prices pr ON pr.tenant_id=p.tenant_id AND pr.product_id=p.product_id AND pr.status='active'
			 WHERE p.tenant_id=$1 AND p.id=$2::uuid AND p.status='active'
			 ORDER BY pr.created_at LIMIT 1`, tenantID, planID).
			Scan(&t.VersionID, &t.PriceID, &t.Currency, &t.Amount)
	})
	if err != nil {
		return nil, fmt.Errorf("read published plan %s: %w", planID, err)
	}
	if t.VersionID != versionID {
		return nil, fmt.Errorf("plan %s current version is %q, want the published %s", planID, t.VersionID, versionID)
	}
	return t, nil
}

func loadSubscribePrefix(ctx context.Context, pool *db.Pool, tenantID string) (string, error) {
	var prefix string
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT coalesce(sub_path_prefix,'') FROM tenants WHERE id=$1`, tenantID).Scan(&prefix)
	})
	return prefix, err
}

// ---------------------------------------------------------------------------
// 一批用户：先在内存里把每张表的列数组排好，再各用一条 unnest 语句写入
// ---------------------------------------------------------------------------

type userBatch struct {
	Start         int
	UserIDs       []string
	Emails        []string
	SubIDs        []string
	Tokens        []string
	TokenHashes   [][]byte
	TokenPrefixes []string
	// Sealed 是信封加密的令牌原文（aad 绑订阅 ID，同 provisionSubscription）；没给主密钥时整列为 nil
	Sealed [][]byte
}

// buildUserBatch 生成第 [start, start+count) 个用户的全部列值。三个生成器可注入，单测用确定值。
func buildUserBatch(ns namespace, start, count int, newID func() (string, error),
	newToken func() (string, error), seal func(token, subID string) ([]byte, error)) (*userBatch, error) {
	b := &userBatch{Start: start}
	for i := start; i < start+count; i++ {
		uid, err := newID()
		if err != nil {
			return nil, err
		}
		sid, err := newID()
		if err != nil {
			return nil, err
		}
		tok, err := newToken()
		if err != nil {
			return nil, err
		}
		if len(tok) < 16 {
			return nil, fmt.Errorf("subscription token too short: %d", len(tok))
		}
		var sealed []byte
		if seal != nil {
			if sealed, err = seal(tok, sid); err != nil {
				return nil, err
			}
		}
		b.UserIDs = append(b.UserIDs, uid)
		b.Emails = append(b.Emails, ns.Email(i))
		b.SubIDs = append(b.SubIDs, sid)
		b.Tokens = append(b.Tokens, tok)
		b.TokenHashes = append(b.TokenHashes, crypto.HashToken(tok))
		b.TokenPrefixes = append(b.TokenPrefixes, tok[:8])
		b.Sealed = append(b.Sealed, sealed)
	}
	return b, nil
}

func newV7() (string, error) {
	id, err := uuid.NewV7()
	return id.String(), err
}

func newSubscriptionToken() (string, error) { return crypto.NewToken(32) }

// stmt 是一条带参数的语句；单测核对占位符个数与参数个数一致。
type stmt struct {
	SQL  string
	Args []any
}

// 用户：照 adminops.GenerateUsers，邮箱视同已验证、状态 active。
func (b *userBatch) usersStmt(tenantID string) stmt {
	return stmt{`
		INSERT INTO users (id, tenant_id, email, email_verified_at, status)
		SELECT u.id, $1::uuid, u.email, now(), 'active'
		  FROM unnest($2::uuid[], $3::text[]) AS u(id, email)`,
		[]any{tenantID, b.UserIDs, b.Emails}}
}

// 口令：全批共用同一条 PHC（同一个虚构口令只哈希一次）。
func (b *userBatch) passwordsStmt(tenantID, phc string) stmt {
	return stmt{`
		INSERT INTO user_passwords (user_id, tenant_id, phc)
		SELECT u.id, $1::uuid, $3::text FROM unnest($2::uuid[]) AS u(id)`,
		[]any{tenantID, b.UserIDs, phc}}
}

// 订阅：先建 pending，RETURNING 拿数据库分配的 node_uid（返回顺序不保证，按 id 对回去）。
func (b *userBatch) subscriptionsStmt(tenantID string, t *planTerms) stmt {
	return stmt{`
		INSERT INTO subscriptions
			(id, tenant_id, user_id, plan_id, plan_version_id, price_id, status,
			 current_period_start, current_period_end, snapshot_currency, snapshot_amount)
		SELECT s.id, $1::uuid, s.user_id, $4::uuid, $5::uuid, $6::uuid, 'pending',
		       $7::timestamptz, $8::timestamptz, $9::app.currency_code, $10::bigint
		  FROM unnest($2::uuid[], $3::uuid[]) AS s(id, user_id)
		RETURNING id::text, node_uid`,
		[]any{tenantID, b.SubIDs, b.UserIDs, t.PlanID, t.VersionID, t.PriceID,
			t.PeriodStart, t.PeriodEnd, t.Currency, t.Amount}}
}

// 激活：经 subscription_transitions 触发器（SUB-004），不直接插 active。
func (b *userBatch) activateStmt(tenantID string) stmt {
	return stmt{`
		UPDATE subscriptions SET status = 'active'
		 WHERE tenant_id = $1::uuid AND id = ANY($2::uuid[]) AND status = 'pending'`,
		[]any{tenantID, b.SubIDs}}
}

// 开通事件：追加写表，与 provisionSubscription 同一组字段；payload 多记一个来源，事后能认出这是压测造数。
func (b *userBatch) eventsStmt(tenantID string, t *planTerms) stmt {
	payload, _ := json.Marshal(map[string]any{"period_end": t.PeriodEnd, "source": "loadtest-seed"})
	return stmt{`
		INSERT INTO subscription_events
			(tenant_id, subscription_id, event_type, from_status, to_status, actor_kind, payload)
		SELECT $1::uuid, s.id, 'activated', 'pending', 'active', 'system', $3::jsonb
		  FROM unnest($2::uuid[]) AS s(id)`,
		[]any{tenantID, b.SubIDs, string(payload)}}
}

// 配额：照 initQuotaBalances，按套餐版本的配额定义逐订阅补齐（total 周期不设截止）。
func (b *userBatch) quotasStmt(tenantID string, t *planTerms) stmt {
	return stmt{`
		INSERT INTO quota_balances
			(tenant_id, subscription_id, metric, period, period_start, period_end, granted, limit_value)
		SELECT $1::uuid, s.id, qd.metric, qd.period, $3::timestamptz,
		       CASE WHEN qd.period = 'total' THEN NULL::timestamptz ELSE $4::timestamptz END,
		       coalesce(qd.limit_value, 0), qd.limit_value
		  FROM unnest($2::uuid[]) AS s(id)
		 CROSS JOIN quota_definitions qd
		 WHERE qd.plan_version_id = $5::uuid`,
		[]any{tenantID, b.SubIDs, t.PeriodStart, t.PeriodEnd, t.VersionID}}
}

// 订阅凭据：库里只有哈希、前缀与（可选的）信封密文，令牌原文只进 manifest。
func (b *userBatch) credentialsStmt(tenantID string, t *planTerms) stmt {
	return stmt{`
		INSERT INTO subscription_credentials
			(tenant_id, subscription_id, user_id, token_hash, token_prefix, scope, expires_at, token_encrypted)
		SELECT $1::uuid, c.sub, c.usr, c.hash, c.prefix, 'subscription', $2::timestamptz, c.sealed
		  FROM unnest($3::uuid[], $4::uuid[], $5::bytea[], $6::text[], $7::bytea[])
		       AS c(sub, usr, hash, prefix, sealed)`,
		[]any{tenantID, t.PeriodEnd, b.SubIDs, b.UserIDs, b.TokenHashes, b.TokenPrefixes, b.Sealed}}
}

// ---------------------------------------------------------------------------
// 写库
// ---------------------------------------------------------------------------

type userSeedInput struct {
	TenantID  string
	NS        namespace
	Terms     *planTerms
	PHC       string
	Users     int
	BatchSize int
	Envelope  *crypto.Envelope
}

// userSeedResult 带回 manifest 用户与两段分项耗时（users = 用户与口令；subscriptions = 订阅、激活、事件、配额、凭据与提交）。
type userSeedResult struct {
	Users         []ltkit.ManifestUser
	UsersTime     time.Duration
	SubsTime      time.Duration
	Subscriptions int
}

func seedUsers(ctx context.Context, pool *db.Pool, in userSeedInput, progress func(done int)) (*userSeedResult, error) {
	var seal func(token, subID string) ([]byte, error)
	if in.Envelope != nil {
		seal = func(token, subID string) ([]byte, error) { return in.Envelope.Seal([]byte(token), []byte(subID)) }
	}
	res := &userSeedResult{Users: make([]ltkit.ManifestUser, 0, in.Users)}
	for start := 0; start < in.Users; start += in.BatchSize {
		count := min(in.BatchSize, in.Users-start)
		b, err := buildUserBatch(in.NS, start, count, newV7, newSubscriptionToken, seal)
		if err != nil {
			return nil, err
		}
		nodeUIDs := make(map[string]int64, count)
		var tUsers, tSubs time.Duration
		err = pool.InTx(ctx, db.Scope{TenantID: in.TenantID}, func(tx pgx.Tx) error {
			t0 := time.Now()
			for _, s := range []stmt{b.usersStmt(in.TenantID), b.passwordsStmt(in.TenantID, in.PHC)} {
				if err := execCount(ctx, tx, s, count); err != nil {
					return err
				}
			}
			t1 := time.Now()
			tUsers = t1.Sub(t0)
			sub := b.subscriptionsStmt(in.TenantID, in.Terms)
			rows, err := tx.Query(ctx, sub.SQL, sub.Args...)
			if err != nil {
				return fmt.Errorf("insert subscriptions: %w", err)
			}
			for rows.Next() {
				var id string
				var uid int64
				if err := rows.Scan(&id, &uid); err != nil {
					rows.Close()
					return err
				}
				nodeUIDs[id] = uid
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return fmt.Errorf("insert subscriptions: %w", err)
			}
			if len(nodeUIDs) != count {
				return fmt.Errorf("inserted %d subscriptions, want %d", len(nodeUIDs), count)
			}
			for _, s := range []stmt{b.activateStmt(in.TenantID), b.eventsStmt(in.TenantID, in.Terms),
				b.credentialsStmt(in.TenantID, in.Terms)} {
				if err := execCount(ctx, tx, s, count); err != nil {
					return err
				}
			}
			q := b.quotasStmt(in.TenantID, in.Terms)
			if _, err := tx.Exec(ctx, q.SQL, q.Args...); err != nil {
				return fmt.Errorf("init quota balances: %w", err)
			}
			tSubs = time.Since(t1)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("users %d..%d: %w", start+1, start+count, err)
		}
		// 提交耗时算进订阅段：提交时要发出整批的变更通知
		res.UsersTime += tUsers
		res.SubsTime += tSubs
		for k := 0; k < count; k++ {
			ip, err := userIP(start + k)
			if err != nil {
				return nil, err
			}
			res.Users = append(res.Users, ltkit.ManifestUser{
				ID: b.UserIDs[k], Email: b.Emails[k], SubscribeToken: b.Tokens[k], RealIP: ip,
				SubscriptionID: b.SubIDs[k], NodeUID: nodeUIDs[b.SubIDs[k]],
			})
		}
		res.Subscriptions += count
		if progress != nil {
			progress(start + count)
		}
	}
	return res, nil
}

func execCount(ctx context.Context, tx pgx.Tx, s stmt, want int) error {
	tag, err := tx.Exec(ctx, s.SQL, s.Args...)
	if err != nil {
		return fmt.Errorf("%s: %w", firstLine(s.SQL), err)
	}
	if got := tag.RowsAffected(); got != int64(want) {
		return fmt.Errorf("%s: affected %d rows, want %d", firstLine(s.SQL), got, want)
	}
	return nil
}

// writeSeedAudit 给整批留一条审计（沿用后台批量生成账号的动作名），事后翻审计能看出这些账号从哪来。
func writeSeedAudit(ctx context.Context, pool *db.Pool, tenantID string, ns namespace, users, nodes int) error {
	return pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "system", ActorLabel: "loadtest-seed",
			Action: "user.bulk_generated", ResourceType: "user", APIDomain: "admin",
			AfterDigest: map[string]any{
				"count": users, "nodes": nodes, "label": ns.Label, "run": ns.Run,
				"domain": loadtestEmailDomain, "reason": "loadtest seed",
			},
		})
	})
}

func firstLine(sql string) string {
	for _, line := range strings.Split(sql, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return sql
}
