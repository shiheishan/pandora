// [INPUT]: 依赖 pgx 事务读取 audit_events，依赖 encoding/json 与 math/big 做摘要规范化
// [OUTPUT]: 对外提供 ChainReport、VerifyChain；包内提供 chainRecord、chainHashV2、chainHashV1、canonicalJSON
// [POS]: platform/audit 的哈希链口径与校验：audit.go 的 Write 用 chainHashV2 算新记录，VerifyChain 按两版口径复算整条链。存量行（00083 之前写入、chain_seq 为空）一律不改写：不带摘要的严格复算，带摘要的先试库里文本与键排序紧凑形，复算不出就只核对 prev_hash 链接并记入 ChainReport.LegacyLinkOnly
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package audit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
)

// ============================================================================
// 两版口径
// ============================================================================
//
// 第一版（chain_seq 为空，00083 之前写入）：各字段直接拼接后求 SHA-256，
// 摘要用写入时 json.Marshal 的原始字节。jsonb 不保留这串字节，所以带摘要的
// 第一版记录大多复算不出来；链的顺序按 occurred_at, id。
//
// 第二版（chain_seq 非空）：
//   - 顺序由 chain_seq 决定，写入端在同租户的 advisory lock 内取「最大值 + 1」；
//   - 摘要先规范化（canonicalJSON）再参与哈希，只依赖 JSON 的值，不依赖
//     jsonb 的键序、空白与数字写法，也不依赖 PostgreSQL 版本的输出格式；
//   - 每个字段带长度前缀、可空字段带存在标记，字段之间不能互相挪字节；
//   - 除第一版的字段外，还绑定链序号、发生时间、操作者标签、请求、来源、
//     审批、错误码与认证强度 —— 写入后改动任何一个都会断链。
//
// 两版之间的衔接：第一条第二版记录的 prev_hash 是按第一版顺序的最后一条
// 记录的 entry_hash，校验时先走完全部第一版记录，再按 chain_seq 走第二版。

const chainV2Domain = "aegis/audit_events/chain/v2"

// chainRecord 是参与第二版哈希的一行，取值与库里存的形态一致：
// 可空文本列存 NULL 时取空串（Write 从不写空串，两者不会混淆）。
type chainRecord struct {
	Seq          int64
	OccurredAt   time.Time
	TenantID     string
	ActorKind    string
	ActorID      *string
	ActorLabel   string
	Action       string
	ResourceType string
	ResourceID   *string
	Before       []byte // JSON 文本；nil 表示 SQL NULL
	After        []byte
	RequestID    string
	APIDomain    string
	SourceIPHash []byte
	UserAgent    string
	ApprovalID   *string
	Outcome      string
	ErrorCode    string
	SourceIPEnc  []byte
	AuthContext  string
}

func chainHashV2(prev []byte, r chainRecord) ([]byte, error) {
	before, err := canonicalOptional(r.Before)
	if err != nil {
		return nil, fmt.Errorf("规范化 before_digest: %w", err)
	}
	after, err := canonicalOptional(r.After)
	if err != nil {
		return nil, fmt.Errorf("规范化 after_digest: %w", err)
	}
	w := chainWriter{sha256.New()}
	w.bytes([]byte(chainV2Domain))
	w.bytes(prev)
	w.u64(uint64(r.Seq))
	w.u64(uint64(r.OccurredAt.UnixMicro()))
	w.str(r.TenantID)
	w.str(r.ActorKind)
	w.optStr(r.ActorID)
	w.str(r.ActorLabel)
	w.str(r.Action)
	w.str(r.ResourceType)
	w.optStr(r.ResourceID)
	w.optBytes(before)
	w.optBytes(after)
	w.str(r.RequestID)
	w.str(r.APIDomain)
	w.bytes(r.SourceIPHash)
	w.str(r.UserAgent)
	w.optStr(r.ApprovalID)
	w.str(r.Outcome)
	w.str(r.ErrorCode)
	w.bytes(r.SourceIPEnc)
	// 第二版恒定参与（空串也算一个值）；第一版「非空才参与」的规则只留给存量行
	w.str(r.AuthContext)
	return w.h.Sum(nil), nil
}

// chainWriter 以「8 字节长度 + 内容」写每个字段，可空字段前再加 1 字节存在标记。
type chainWriter struct{ h hash.Hash }

func (w chainWriter) u64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	w.h.Write(b[:])
}

func (w chainWriter) bytes(b []byte) {
	w.u64(uint64(len(b)))
	w.h.Write(b)
}

func (w chainWriter) str(s string) { w.bytes([]byte(s)) }

func (w chainWriter) optBytes(b []byte) {
	if b == nil {
		w.h.Write([]byte{0})
		return
	}
	w.h.Write([]byte{1})
	w.bytes(b)
}

func (w chainWriter) optStr(p *string) {
	if p == nil {
		w.optBytes(nil)
		return
	}
	w.optBytes([]byte(*p))
}

// chainHashV1 是第一版口径，只用于复算存量行，不得改动。
func chainHashV1(prev []byte, tenantID string, e Entry, before, after []byte) []byte {
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

// ============================================================================
// 摘要规范化
// ============================================================================

// canonicalJSON 把一段 JSON 写成只取决于其值的确定形式：对象键按字节序
// 排列、无空白、字符串按 encoding/json 转义、数字写成最简分数（big.Rat）。
// 数字这样处理是因为 jsonb 以 numeric 保存数字：1e2 读回来是 100，
// 1.50 读回来仍是 1.50，写法会变，值不会变。
func canonicalJSON(raw []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON 之后还有多余内容")
	}
	var buf bytes.Buffer
	if err := writeCanonical(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func canonicalOptional(raw []byte) ([]byte, error) {
	if raw == nil {
		return nil, nil
	}
	return canonicalJSON(raw)
}

func writeCanonical(buf *bytes.Buffer, v any) error {
	switch x := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if x {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		b, err := json.Marshal(x)
		if err != nil {
			return err
		}
		buf.Write(b)
	case json.Number:
		r, ok := new(big.Rat).SetString(string(x))
		if !ok {
			return fmt.Errorf("无法解析的数字 %q", string(x))
		}
		buf.WriteString(r.RatString())
	case []any:
		buf.WriteByte('[')
		for i, item := range x {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, item); err != nil {
				return err
			}
		}
		buf.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		buf.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				buf.WriteByte(',')
			}
			if err := writeCanonical(buf, k); err != nil {
				return err
			}
			buf.WriteByte(':')
			if err := writeCanonical(buf, x[k]); err != nil {
				return err
			}
		}
		buf.WriteByte('}')
	default:
		return fmt.Errorf("未知的 JSON 值类型 %T", v)
	}
	return nil
}

// ============================================================================
// 校验
// ============================================================================

// ChainReport 是一次整链校验的结果。BrokenAt 为空表示链条完整。
type ChainReport struct {
	// Rows 是校验到的行数（断点之前与断点本身）
	Rows int
	// LegacyLinkOnly 是第一版存量行中「带摘要、内容复算不出、只核对了链接」的行数。
	// 这些行的删除、插入、换位与改 entry_hash 仍会断链，但摘要与其余字段被改不会。
	LegacyLinkOnly int
	// BrokenAt 是第一处断点所在记录的 id
	BrokenAt string
	// Reason 说明断点性质：link（prev_hash 不接上一条）、seq（链序号不连续）、
	// hash（内容复算不符）
	Reason string
}

// VerifyChain 重算租户的整条审计链，返回第一处断点。供 SEC-016 的取证与季度演练使用。
//
// 规则：
//   - 先按 occurred_at, id 走第一版行，再按 chain_seq 走第二版行；每一行的
//     prev_hash 必须等于上一行的 entry_hash（第一行为空）；
//   - 第二版行：chain_seq 从 1 起连续，内容按第二版口径严格复算；
//   - 第一版行：按第一版口径复算，摘要依次试库里读回的文本与「按键排序的
//     紧凑 JSON」（encoding/json 编码 map 的形态）；都不符时，带摘要的行记入
//     LegacyLinkOnly 放行，不带摘要的行判为断点 —— 不带摘要的行一定复算得出。
//
// 链尾被整条删掉无法从链本身发现，这是哈希链的固有边界。
func VerifyChain(ctx context.Context, tx pgx.Tx, tenantID string) (ChainReport, error) {
	rows, err := tx.Query(ctx, `
		SELECT id::text, chain_seq, occurred_at, tenant_id::text, actor_kind, actor_id::text,
		       coalesce(actor_label, ''), action, coalesce(resource_type, ''), resource_id::text,
		       before_digest::text, after_digest::text, coalesce(request_id, ''),
		       coalesce(api_domain, ''), source_ip_hash, coalesce(user_agent, ''),
		       approval_request_id::text, outcome, coalesce(error_code, ''), source_ip_enc,
		       coalesce(auth_context, ''), prev_hash, entry_hash
		  FROM audit_events
		 WHERE tenant_id = $1
		 ORDER BY chain_seq NULLS FIRST, occurred_at, id`, tenantID)
	if err != nil {
		return ChainReport{}, err
	}
	defer rows.Close()

	var (
		report  ChainReport
		prev    []byte
		wantSeq int64 = 1
	)
	broken := func(id, reason string) (ChainReport, error) {
		report.BrokenAt, report.Reason = id, reason
		return report, nil
	}
	for rows.Next() {
		var (
			id                 string
			seq                *int64
			r                  chainRecord
			before, after      *string
			storedPrev, stored []byte
		)
		if err := rows.Scan(&id, &seq, &r.OccurredAt, &r.TenantID, &r.ActorKind, &r.ActorID,
			&r.ActorLabel, &r.Action, &r.ResourceType, &r.ResourceID,
			&before, &after, &r.RequestID,
			&r.APIDomain, &r.SourceIPHash, &r.UserAgent,
			&r.ApprovalID, &r.Outcome, &r.ErrorCode, &r.SourceIPEnc,
			&r.AuthContext, &storedPrev, &stored); err != nil {
			return ChainReport{}, err
		}
		report.Rows++
		r.Before, r.After = textBytes(before), textBytes(after)

		if !bytes.Equal(storedPrev, prev) {
			return broken(id, "link")
		}
		if seq != nil {
			if *seq != wantSeq {
				return broken(id, "seq")
			}
			wantSeq++
			r.Seq = *seq
			want, err := chainHashV2(prev, r)
			if err != nil || !bytes.Equal(want, stored) {
				return broken(id, "hash")
			}
		} else {
			switch verifyLegacy(prev, tenantID, r, stored) {
			case legacyMismatch:
				return broken(id, "hash")
			case legacyLinkOnly:
				report.LegacyLinkOnly++
			}
		}
		prev = stored
	}
	return report, rows.Err()
}

type legacyResult int

const (
	legacyVerified legacyResult = iota
	legacyLinkOnly
	legacyMismatch
)

func verifyLegacy(prev []byte, tenantID string, r chainRecord, stored []byte) legacyResult {
	e := Entry{ActorKind: r.ActorKind, ActorID: r.ActorID, Action: r.Action,
		ResourceType: r.ResourceType, ResourceID: r.ResourceID, Outcome: r.Outcome,
		AuthContext: r.AuthContext}
	for _, form := range []func([]byte) []byte{identityForm, sortedCompactForm} {
		if bytes.Equal(chainHashV1(prev, tenantID, e, form(r.Before), form(r.After)), stored) {
			return legacyVerified
		}
	}
	if r.Before != nil || r.After != nil {
		return legacyLinkOnly
	}
	return legacyMismatch
}

func identityForm(b []byte) []byte { return b }

// sortedCompactForm 重现 json.Marshal 编码 map[string]any 的形态：键按字节序、
// 无空白、HTML 字符转义。第一版摘要多数是 map，这样能把它们找回严格复算。
func sortedCompactForm(b []byte) []byte {
	if b == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return b
	}
	out, err := json.Marshal(v)
	if err != nil {
		return b
	}
	return out
}

func textBytes(s *string) []byte {
	if s == nil {
		return nil
	}
	return []byte(*s)
}
