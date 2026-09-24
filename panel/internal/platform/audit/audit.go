// [INPUT]: 依赖 platform/httpx 的来源信息与调用主体（ClientIPFrom / UserAgentFrom / PrincipalFrom），依赖 pgx 事务
// [OUTPUT]: 对外提供 Entry、Configure、Write、VerifyChain
// [POS]: platform 的审计写入唯一入口，全部领域的审计都经 Write 进 audit_events；auth_context（00080）在这里从主体推出
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

// Package audit 写入不可删审计记录（SEC-012）。
//
// 除了数据库层的追加写触发器，这里再加一层哈希链：
// 每条记录的摘要都把上一条的哈希算进去。这样「不可删」从一条权限约束
// 升级为可数学验证的性质 —— 即便有人拿到了 superuser 删掉中间某条，
// 链条断裂也会在校验时暴露，而不是无声无息。
package audit

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

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

	// 串行化同租户的审计写入，保证哈希链不分叉。
	// 用事务级 advisory lock：随事务结束自动释放，不会泄漏锁。
	// 审计不是热路径，这点串行开销换来链条完整性是划算的。
	lockKey := int64(binary.BigEndian.Uint64(sha256Sum(tenantID)[:8]) >> 1)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, lockKey); err != nil {
		return fmt.Errorf("获取审计链锁: %w", err)
	}

	var prevHash []byte
	err := tx.QueryRow(ctx, `
		SELECT entry_hash FROM audit_events
		 WHERE tenant_id = $1
		 ORDER BY occurred_at DESC, id DESC
		 LIMIT 1`, tenantID).Scan(&prevHash)
	if err != nil && err != pgx.ErrNoRows {
		return fmt.Errorf("读取审计链尾: %w", err)
	}

	beforeJSON, err := toJSON(e.BeforeDigest)
	if err != nil {
		return err
	}
	afterJSON, err := toJSON(e.AfterDigest)
	if err != nil {
		return err
	}

	entryHash := chainHash(prevHash, tenantID, e, beforeJSON, afterJSON)

	_, err = tx.Exec(ctx, `
		INSERT INTO audit_events
			(tenant_id, actor_kind, actor_id, actor_label, action,
			 resource_type, resource_id, before_digest, after_digest,
			 request_id, api_domain, source_ip_hash, user_agent,
			 approval_request_id, outcome, error_code, prev_hash, entry_hash,
			 source_ip_enc, auth_context)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		tenantID, e.ActorKind, e.ActorID, nullIfEmpty(e.ActorLabel), e.Action,
		nullIfEmpty(e.ResourceType), e.ResourceID, beforeJSON, afterJSON,
		nullIfEmpty(e.RequestID), nullIfEmpty(e.APIDomain), e.SourceIPHash,
		nullIfEmpty(e.UserAgent), e.ApprovalID, e.Outcome, nullIfEmpty(e.ErrorCode),
		prevHash, entryHash, ipEnc, nullIfEmpty(e.AuthContext))
	if err != nil {
		return fmt.Errorf("写入审计记录: %w", err)
	}
	return nil
}

// VerifyChain 重算整条链并返回第一处断裂的记录 ID。
// 返回空串表示链条完整。供 SEC-016 的取证与季度演练使用。
func VerifyChain(ctx context.Context, tx pgx.Tx, tenantID string) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, actor_kind, actor_id, action, resource_type, resource_id,
		       before_digest, after_digest, outcome, coalesce(auth_context, ''),
		       prev_hash, entry_hash
		  FROM audit_events
		 WHERE tenant_id = $1
		 ORDER BY occurred_at, id`, tenantID)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var prev []byte
	for rows.Next() {
		var (
			id, actorKind, action, outcome    string
			authContext                       string
			actorID, resourceType, resourceID *string
			beforeJSON, afterJSON             []byte
			storedPrev, storedHash            []byte
		)
		if err := rows.Scan(&id, &actorKind, &actorID, &action, &resourceType,
			&resourceID, &beforeJSON, &afterJSON, &outcome, &authContext,
			&storedPrev, &storedHash); err != nil {
			return "", err
		}

		e := Entry{ActorKind: actorKind, ActorID: actorID, Action: action, Outcome: outcome,
			AuthContext: authContext}
		if resourceType != nil {
			e.ResourceType = *resourceType
		}
		e.ResourceID = resourceID

		want := chainHash(prev, tenantID, e, beforeJSON, afterJSON)
		if !equalBytes(want, storedHash) {
			return id, nil
		}
		prev = storedHash
	}
	return "", rows.Err()
}

func chainHash(prev []byte, tenantID string, e Entry, before, after []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	h.Write([]byte(tenantID))
	h.Write([]byte(e.ActorKind))
	if e.ActorID != nil {
		h.Write([]byte(*e.ActorID))
	}
	h.Write([]byte(e.Action))
	h.Write([]byte(e.ResourceType))
	if e.ResourceID != nil {
		h.Write([]byte(*e.ResourceID))
	}
	h.Write(before)
	h.Write(after)
	h.Write([]byte(e.Outcome))
	// 只在非空时参与：00080 之前的记录没有这一列，它们的哈希必须原样可复算
	if e.AuthContext != "" {
		h.Write([]byte(e.AuthContext))
	}
	return h.Sum(nil)
}

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

func sha256Sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
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

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
