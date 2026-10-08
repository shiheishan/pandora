// Package audit 写入不可删审计记录（SEC-012）。
//
// 除了数据库层的追加写触发器，这里再加一层哈希链：
// 每条记录的摘要都把上一条的哈希算进去。这样「不可删」从一条权限约束
// 升级为可数学验证的性质 —— 即便有人拿到了 superuser 删掉中间某条，
// 链条断裂也会在校验时暴露，而不是无声无息。
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type Entry struct {
	ActorKind    string // user / admin / system / agent / plugin / anonymous
	ActorID      *string
	ActorLabel   string
	Action       string
	ResourceType string
	ResourceID   *string
	BeforeDigest any
	AfterDigest  any
	RequestID    string
	APIDomain    string // public / admin / client / node
	// SourceIPHash 是调用方已经算好的哈希（老调用点走这条）。
	SourceIPHash []byte
	// SourceIP 是明文来源地址。填了它，Write 会自己算哈希并加密一份 ——
	// 新的调用点应当用这个，把「怎么哈希、怎么加密」收在一处，
	// 避免各调用点各算各的、盐还不一样。
	SourceIP   string
	UserAgent  string
	ApprovalID *string
	Outcome    string // success / failure / denied / partial
	ErrorCode  string
	// AuthContext 是操作者此刻的认证强度：session / reauth，空表示不适用。
	// 调用点一般不填，由 Write 从 context 的主体推出（见 authContextFrom）。
	AuthContext string
}

// 来源信息的哈希盐与加密器，由 main 在启动时注入。
//
// 做成包级变量而不是每次调用传参：审计有 19 个调用点，
// 让每一处都记得传盐和加密器，迟早会有人忘掉，
// 而忘掉的表现是那条记录悄悄少了来源信息 —— 没人会发现。
var (
	ipHasher func(string) []byte
	ipSealer func([]byte) ([]byte, error)
)

// Configure 注入来源信息的处理方式。未调用时退化为只记调用方给的哈希。
//
// 传入的是哈希函数而不是盐：项目里已经有 crypto.HashIdentifier 在算 IP 哈希，
// 这里另起一套盐会让新旧记录的哈希值对不上 —— 那样按 IP 反查账号时，
// 迁移前后的数据会被当成两个不同的 IP，关联分析恰好在最需要它的
// 历史区间断掉，而且断得毫无征兆。
func Configure(hasher func(string) []byte, sealer func([]byte) ([]byte, error)) {
	ipHasher = hasher
	ipSealer = sealer
}

// Write 在给定事务中追加一条审计记录。
//
// 必须与业务写在同一个事务里：审计与业务同生共死，
// 不存在「业务成功但没留下痕迹」或「留下痕迹但业务回滚了」的窗口。
func Write(ctx context.Context, tx pgx.Tx, tenantID string, e Entry) error {
	if e.Outcome == "" {
		e.Outcome = "success"
	}

	// 调用点没显式给来源信息时，从 context 兜底。
	//
	// 显式传入的优先：登录失败这类场景下，调用点拿到的 IP 比 context 里的
	// 更贴近事实（例如批量任务代表某个用户重放一次操作）。
	if e.SourceIP == "" && len(e.SourceIPHash) == 0 {
		e.SourceIP = httpx.ClientIPFrom(ctx)
	}
	if e.UserAgent == "" {
		e.UserAgent = httpx.UserAgentFrom(ctx)
	}
	if e.AuthContext == "" {
		e.AuthContext = authContextFrom(ctx, e.ActorID)
	}

	// 明文 IP 优先：算哈希用于关联分析，加密一份供后台查看
	var ipEnc []byte
	if e.SourceIP != "" {
		if ipHasher != nil {
			if h := ipHasher(e.SourceIP); len(h) > 0 {
				e.SourceIPHash = h
			}
		}
		if ipSealer != nil {
			// 加密失败不该让业务操作跟着失败 —— 少一份可读的来源信息，
			// 远好过因为审计写不进去而回滚一笔订单
			if enc, err := ipSealer([]byte(e.SourceIP)); err == nil {
				ipEnc = enc
			}
		}
	}

	// 取号：本租户链头的 last_seq 加一，同时拿到前驱（链尾）的 entry_hash。
	//
	// 链头一行一个租户（00142）。读已提交事务里 UPDATE 等到行锁后作用在最新版本上，
	// 序号与前驱都是最新的，行锁顺带串行化同租户的写；可串行化事务的快照早于这一句，
	// 别人在快照之后取过号就直接报 40001（不会再读到旧链尾、写出重复序号），由
	// InTxSerializableRetry 重试。序列化事务取号前先过审计链闸（读已提交事务不碰），
	// 有重试在排队时立刻让路，见 db.EnterChainGate。
	if err := db.EnterChainGate(ctx, tx); err != nil {
		return fmt.Errorf("取审计链序号: %w", err)
	}
	//
	// 发生时间与 uuid 的规范文本在同一句里取回：uuid 经 PostgreSQL 往返一次，库里存的
	// 是 uuid 值，读回永远是小写带连字符的形式，调用方给的大写或不带连字符的写法不能
	// 直接进哈希。发生时间显式取 now() 再写回列里（与列默认值相同），这样哈希里的
	// 时间就是库里那一个。
	var (
		rec      chainRecord
		prevHash []byte
	)
	args := []any{tenantID, e.ActorID, e.ResourceID, e.ApprovalID}
	scan := func(row pgx.Row) error {
		return row.Scan(&rec.Seq, &prevHash,
			&rec.OccurredAt, &rec.TenantID, &rec.ActorID, &rec.ResourceID, &rec.ApprovalID)
	}
	err := scan(tx.QueryRow(ctx, claimSeqSQL, args...))
	if errors.Is(err, pgx.ErrNoRows) {
		// 本租户还没有链头：按审计表的链尾建一个（并发建头的另一方走 ON CONFLICT 取下一个号）
		err = scan(tx.QueryRow(ctx, createHeadSQL, args...))
	}
	if err != nil {
		return fmt.Errorf("取审计链序号: %w", err)
	}

	beforeJSON, err := toJSON(e.BeforeDigest)
	if err != nil {
		return err
	}
	afterJSON, err := toJSON(e.AfterDigest)
	if err != nil {
		return err
	}

	rec.ActorKind, rec.ActorLabel, rec.Action = e.ActorKind, e.ActorLabel, e.Action
	rec.ResourceType, rec.Before, rec.After = e.ResourceType, beforeJSON, afterJSON
	rec.RequestID, rec.APIDomain, rec.SourceIPHash = e.RequestID, e.APIDomain, e.SourceIPHash
	rec.UserAgent, rec.Outcome, rec.ErrorCode = e.UserAgent, e.Outcome, e.ErrorCode
	rec.SourceIPEnc, rec.AuthContext = ipEnc, e.AuthContext
	entryHash, err := chainHashV2(prevHash, rec)
	if err != nil {
		return err
	}

	// 写行与把链头的 last_hash 推到新行是同一条语句：链头与链尾在任何提交点上都一致
	tag, err := tx.Exec(ctx, appendEventSQL,
		tenantID, rec.ActorKind, rec.ActorID, nullIfEmpty(rec.ActorLabel), rec.Action,
		nullIfEmpty(rec.ResourceType), rec.ResourceID, beforeJSON, afterJSON,
		nullIfEmpty(rec.RequestID), nullIfEmpty(rec.APIDomain), rec.SourceIPHash,
		nullIfEmpty(rec.UserAgent), rec.ApprovalID, rec.Outcome, nullIfEmpty(rec.ErrorCode),
		prevHash, entryHash, ipEnc, nullIfEmpty(rec.AuthContext), rec.Seq, rec.OccurredAt)
	if err != nil {
		return fmt.Errorf("写入审计记录: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return errors.New("写入审计记录: 链头不见了")
	}
	return nil
}

// claimSeqSQL 取号：链头 last_seq 加一，返回新序号、前驱哈希，以及规范化的
// 发生时间与 uuid 文本（参数：租户、操作者、资源、审批单）。
const claimSeqSQL = `
	UPDATE audit_chain_heads SET last_seq = last_seq + 1
	 WHERE tenant_id = $1
	RETURNING last_seq, last_hash,
	          now(), $1::uuid::text, $2::uuid::text, $3::uuid::text, $4::uuid::text`

// createHeadSQL 给还没有链头的租户建头，返回值与 claimSeqSQL 相同。
//
// 链尾按审计表现算：有第二版记录就接最大 chain_seq 那一条；没有就接第一版链尾
// （00086 之前的记录按 occurred_at, id 排序，与 VerifyChain 走第一版行的顺序一致），
// 序号从 1 起；都没有则前驱为空。另一方并发建了头时走 ON CONFLICT 在它之后取号：
// 读已提交下等它提交后作用在最新版本上，可串行化下报 40001。
const createHeadSQL = `
	INSERT INTO audit_chain_heads AS h (tenant_id, last_seq, last_hash)
	SELECT $1::uuid, coalesce(v2.chain_seq, 0) + 1,
	       CASE WHEN v2.chain_seq IS NOT NULL THEN v2.entry_hash ELSE v1.entry_hash END
	  FROM (SELECT 1) AS one
	  LEFT JOIN LATERAL (
	        SELECT chain_seq, entry_hash FROM audit_events
	         WHERE tenant_id = $1 AND chain_seq IS NOT NULL
	         ORDER BY chain_seq DESC
	         LIMIT 1) AS v2 ON true
	  LEFT JOIN LATERAL (
	        SELECT entry_hash FROM audit_events
	         WHERE tenant_id = $1 AND v2.chain_seq IS NULL
	         ORDER BY occurred_at DESC, id DESC
	         LIMIT 1) AS v1 ON true
	ON CONFLICT (tenant_id) DO UPDATE SET last_seq = h.last_seq + 1
	RETURNING last_seq, last_hash,
	          now(), $1::uuid::text, $2::uuid::text, $3::uuid::text, $4::uuid::text`

// appendEventSQL 写审计行，并把链头的 last_hash 推到这一行（$18 是本行 entry_hash）。
const appendEventSQL = `
	WITH ins AS (
		INSERT INTO audit_events
			(tenant_id, actor_kind, actor_id, actor_label, action,
			 resource_type, resource_id, before_digest, after_digest,
			 request_id, api_domain, source_ip_hash, user_agent,
			 approval_request_id, outcome, error_code, prev_hash, entry_hash,
			 source_ip_enc, auth_context, chain_seq, occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22))
	UPDATE audit_chain_heads SET last_hash = $18
	 WHERE tenant_id = $1 AND last_seq = $21`

// authContextFrom 从请求主体推出本条记录的认证强度。
//
// 只有「主体就是这条记录的操作者、且带着一个登录会话」时才有意义：系统任务
// 没有主体；管理员替用户记的账（actor 是用户）也不该把管理员的认证强度记到
// 用户头上。其余一律留空，比猜一个值诚实。
func authContextFrom(ctx context.Context, actorID *string) string {
	p := httpx.PrincipalFrom(ctx)
	if p.IsAnonymous() || p.SessionID == "" || actorID == nil || *actorID != p.UserID {
		return ""
	}
	if p.ReauthedRecently {
		return "reauth"
	}
	return "session"
}

func toJSON(v any) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("序列化审计摘要: %w", err)
	}
	return b, nil
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
