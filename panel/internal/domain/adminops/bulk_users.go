// [INPUT]: 依赖 users.go 的 currentSubscriptionSQL / subStateSQL，依赖 platform 的 crypto/db/audit/httpx
// [OUTPUT]: 对外提供 BulkFilter、BulkPreview、BulkSampleRow、ExportRow 与 Service.PreviewBulk / ExportUsers / GenerateUsers
// [POS]: domain/adminops 的用户批量运营：预览、导出、群发共用 buildFilterSQL 圈人（含当前订阅的套餐、到期天数与订阅状态），批量生成账号
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"crypto/rand"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 用户批量运营（对标 Xboard user/generate、user/dumpCSV、user/sendMail）。
//
// 三件事共用一个前提：先想清楚「这一批到底是谁」。所以每个操作都先
// 按同一套筛选条件圈定人群，再执行动作 —— 而不是各写各的查询。
// 不这么做的话，「导出的名单」和「群发的名单」会因为条件写法不同而对不上，
// 运营发完邮件才发现漏了人。

// BulkFilter 圈定人群。空条件表示不限。
type BulkFilter struct {
	Status       string // active / suspended / banned / pending
	GroupID      string // 用户分组
	HasActiveSub *bool  // 有无生效订阅
	Query        string // 邮箱模糊匹配
	// 以下三项按「当前订阅」判断，挑法与用户列表的 current_subscription 相同
	PlanID            string // 当前订阅的套餐
	ExpiresWithinDays int    // 当前订阅在 N 天内到期（1–365，0 不限）
	SubState          string // active / expired / none
}

// buildFilterSQL 把筛选条件翻成 WHERE 片段。
//
// 导出、群发、预览三处共用它。共用不是为了少写几行，
// 是为了让「预览说会影响 300 人」和「实际发给 300 人」永远是同一批人。
func buildFilterSQL(f BulkFilter, args *[]any, tenantID string) string {
	*args = append(*args, tenantID)
	where := " WHERE u.tenant_id = $1 AND u.status <> 'anonymized'"

	if f.Status != "" {
		*args = append(*args, f.Status)
		where += " AND u.status = $" + itoa(len(*args))
	}
	if f.GroupID != "" {
		*args = append(*args, f.GroupID)
		where += " AND u.user_group_id = $" + itoa(len(*args)) + "::uuid"
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		*args = append(*args, "%"+strings.ToLower(q)+"%")
		where += " AND lower(u.email::text) LIKE $" + itoa(len(*args))
	}
	if f.HasActiveSub != nil {
		cond := "EXISTS"
		if !*f.HasActiveSub {
			cond = "NOT EXISTS"
		}
		where += " AND " + cond + " (SELECT 1 FROM subscriptions s" +
			" WHERE s.tenant_id = u.tenant_id AND s.user_id = u.id AND s.status = 'active')"
	}
	if f.PlanID != "" {
		*args = append(*args, f.PlanID)
		where += " AND " + currentSubscriptionSQL("plan_id") + " = $" + itoa(len(*args)) + "::uuid"
	}
	if f.ExpiresWithinDays > 0 {
		*args = append(*args, f.ExpiresWithinDays)
		end := currentSubscriptionSQL("current_period_end")
		where += " AND " + end + " >= now() AND " + end + " < now() + make_interval(days => $" + itoa(len(*args)) + "::int)"
	}
	if f.SubState != "" {
		*args = append(*args, f.SubState)
		where += " AND " + subStateSQL("$"+itoa(len(*args))+"::text")
	}
	return where
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (f BulkFilter) validate() error {
	switch f.Status {
	case "", "pending", "active", "suspended", "banned", "deletion_scheduled":
	default:
		return httpx.Invalid(map[string]string{"status": "不支持的用户状态"})
	}
	fields := map[string]string{}
	if f.GroupID != "" {
		if err := validateUUID(f.GroupID); err != nil {
			fields["group_id"] = "分组标识格式不正确"
		}
	}
	if f.PlanID != "" {
		if _, err := uuid.Parse(f.PlanID); err != nil {
			fields["plan_id"] = "套餐标识格式不正确"
		}
	}
	if f.ExpiresWithinDays < 0 || f.ExpiresWithinDays > 365 {
		fields["expires_within_days"] = "到期天数只能是 1–365"
	}
	if !validSubState(f.SubState) {
		fields["sub_state"] = "订阅状态只能是 active、expired 或 none"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

func validateUUID(s string) error {
	if len(s) != 36 {
		return errors.New("bad uuid")
	}
	return nil
}

//-----------------------------------------------------------------------------
// 预览影响面
//-----------------------------------------------------------------------------

type BulkPreview struct {
	Total   int      `json:"total"`
	Samples []string `json:"samples"` // 前若干个邮箱，让人确认圈对了没
	// SampleRows 是同一批样本带上当前订阅的套餐与到期（预览列表用）
	SampleRows []BulkSampleRow `json:"sample_rows"`
}

type BulkSampleRow struct {
	Email            string     `json:"email"`
	PlanName         *string    `json:"plan_name"`
	CurrentPeriodEnd *time.Time `json:"current_period_end"`
}

// PreviewBulk 先告诉管理员这一批是多少人、都有谁。
//
// 群发邮件和导出都是不可撤销的：邮件发出去收不回来，
// 导出的名单一旦落到本地就不知道会流去哪。所以必须先看清影响面。
func (s *Service) PreviewBulk(ctx context.Context, tenantID string,
	f BulkFilter) (*BulkPreview, error) {

	if err := f.validate(); err != nil {
		return nil, err
	}
	out := &BulkPreview{Samples: []string{}, SampleRows: []BulkSampleRow{}}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var args []any
		where := buildFilterSQL(f, &args, tenantID)
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM users u"+where, args...).Scan(&out.Total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			"SELECT u.email::text, (SELECT pl.name FROM plans pl WHERE pl.id = "+currentSubscriptionSQL("plan_id")+"), "+
				currentSubscriptionSQL("current_period_end")+
				" FROM users u"+where+" ORDER BY u.created_at DESC LIMIT 10",
			args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r BulkSampleRow
			if err := rows.Scan(&r.Email, &r.PlanName, &r.CurrentPeriodEnd); err != nil {
				return err
			}
			out.Samples = append(out.Samples, r.Email)
			out.SampleRows = append(out.SampleRows, r)
		}
		return rows.Err()
	})
	return out, err
}

//-----------------------------------------------------------------------------
// 导出
//-----------------------------------------------------------------------------

type ExportRow struct {
	Email       string
	Status      string
	GroupName   string
	ActiveSubs  int
	TotalOrders int
	PaidAmount  int64
	CreatedAt   time.Time
	LastLoginAt *time.Time
}

// ExportUsers 导出用户名单。
//
// 刻意不导出任何可以直接拿来登录或联系的东西之外的隐私字段：
// 没有 IP、没有设备、没有订阅凭据。一份用户导出表最常见的去处是
// 某个人的桌面，然后是某个群 —— 少一个字段就少一分风险。
func (s *Service) ExportUsers(ctx context.Context, tenantID string,
	f BulkFilter, limit int) ([]ExportRow, error) {

	if err := f.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50000 {
		limit = 10000
	}
	out := []ExportRow{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var args []any
		where := buildFilterSQL(f, &args, tenantID)
		args = append(args, limit)
		rows, err := tx.Query(ctx, `
			SELECT u.email::text, u.status, coalesce(g.name,''),
			       (SELECT count(*) FROM subscriptions s
			         WHERE s.tenant_id=u.tenant_id AND s.user_id=u.id AND s.status='active'),
			       (SELECT count(*) FROM orders o
			         WHERE o.tenant_id=u.tenant_id AND o.user_id=u.id),
			       (SELECT coalesce(sum(o.paid_amount),0) FROM orders o
			         WHERE o.tenant_id=u.tenant_id AND o.user_id=u.id
			           AND o.status IN ('paid','fulfilled')),
			       u.created_at, u.last_login_at
			  FROM users u
			  LEFT JOIN user_groups g ON g.tenant_id=u.tenant_id AND g.id=u.user_group_id`+
			where+` ORDER BY u.created_at DESC LIMIT $`+itoa(len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r ExportRow
			if err := rows.Scan(&r.Email, &r.Status, &r.GroupName, &r.ActiveSubs,
				&r.TotalOrders, &r.PaidAmount, &r.CreatedAt, &r.LastLoginAt); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

//-----------------------------------------------------------------------------
// 批量生成账号
//-----------------------------------------------------------------------------

type GenerateUsersInput struct {
	Count       int
	EmailPrefix string
	EmailDomain string
	GroupID     string
	ActorID     string
	Reason      string
}

type GeneratedUser struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// GenerateUsers 批量造账号。
//
// 用途是给经销商或线下渠道预制一批可以直接交付的账号。
//
// 密码只在这一次返回，之后无从取回 —— 库里存的是哈希。
// 所以调用方必须当场保存，界面上也会强调这一点。
func (s *Service) GenerateUsers(ctx context.Context, tenantID string,
	in GenerateUsersInput) ([]GeneratedUser, error) {

	in.EmailPrefix = strings.ToLower(strings.TrimSpace(in.EmailPrefix))
	in.EmailDomain = strings.ToLower(strings.TrimSpace(in.EmailDomain))
	in.Reason = strings.TrimSpace(in.Reason)

	if in.Count < 1 || in.Count > 500 {
		return nil, httpx.Invalid(map[string]string{
			"count": "一次生成 1 到 500 个。更多请分批 —— 单次几千个会把事务拖很久"})
	}
	if !isSafeSlug(in.EmailPrefix) {
		return nil, httpx.Invalid(map[string]string{
			"email_prefix": "前缀只能用小写字母、数字和短横线，1 到 20 位"})
	}
	if !isSafeDomain(in.EmailDomain) {
		return nil, httpx.Invalid(map[string]string{
			"email_domain": "域名格式不正确"})
	}
	if n := utf8.RuneCountInString(in.Reason); n < 5 || n > 500 {
		return nil, httpx.Invalid(map[string]string{
			"reason": "请写清生成原因，5 到 500 个字"})
	}
	if in.GroupID != "" && validateUUID(in.GroupID) != nil {
		return nil, httpx.Invalid(map[string]string{"group_id": "分组标识格式不正确"})
	}

	out := make([]GeneratedUser, 0, in.Count)
	actor := in.ActorID
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actor}, func(tx pgx.Tx) error {
		var group any
		if in.GroupID != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `
				SELECT EXISTS(SELECT 1 FROM user_groups
				               WHERE tenant_id=$1 AND id=$2::uuid)`,
				tenantID, in.GroupID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return httpx.Invalid(map[string]string{"group_id": "分组不存在"})
			}
			group = in.GroupID
		}

		for len(out) < in.Count {
			suffix, err := randomSlug(8)
			if err != nil {
				return err
			}
			email := in.EmailPrefix + "-" + suffix + "@" + in.EmailDomain
			password, err := randomPassword()
			if err != nil {
				return err
			}
			phc, err := crypto.HashPassword(password, crypto.DefaultArgon2Params())
			if err != nil {
				return err
			}

			var userID string
			err = tx.QueryRow(ctx, `
				INSERT INTO users (tenant_id, email, status, user_group_id, email_verified_at)
				VALUES ($1,$2,'active',$3::uuid, now())
				ON CONFLICT (tenant_id, email) DO NOTHING
				RETURNING id::text`, tenantID, email, group).Scan(&userID)
			if errors.Is(err, pgx.ErrNoRows) {
				// 撞了已有邮箱，换一个后缀重试。8 位随机撞车概率极低，
				// 但批量 500 个时「极低」不等于「不会」。
				continue
			}
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `
				INSERT INTO user_passwords (user_id, tenant_id, phc)
				VALUES ($1::uuid,$2,$3)`, userID, tenantID, phc); err != nil {
				return err
			}
			out = append(out, GeneratedUser{Email: email, Password: password})
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: &actor,
			Action: "user.bulk_generated", ResourceType: "user",
			AfterDigest: map[string]any{
				"count": in.Count, "prefix": in.EmailPrefix,
				"domain": in.EmailDomain, "group_id": in.GroupID, "reason": in.Reason,
			},
			APIDomain: "admin", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func isSafeSlug(s string) bool {
	if len(s) < 1 || len(s) > 20 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '-' {
			return false
		}
	}
	return true
}

func isSafeDomain(s string) bool {
	if len(s) < 4 || len(s) > 63 || !strings.Contains(s, ".") {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z') && !(c >= '0' && c <= '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

func randomSlug(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyz23456789"
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, n)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}

// randomPassword 生成一次性初始口令。
//
// 混合大小写、数字和符号是为了满足口令策略；长度 16 是因为这些账号
// 会被明文交到渠道手里，短口令在流转过程中更容易被记下来复用。
func randomPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789@#%+="
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 16)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	// 保证至少各有一个字母和数字：随机串理论上可能全是符号，
	// 那种口令过不了策略校验，用户拿到手也登不进去。
	out[0] = 'A' + byte(int(b[0])%26)
	out[1] = 'a' + byte(int(b[1])%26)
	out[2] = '2' + byte(int(b[2])%8)
	return string(out), nil
}
