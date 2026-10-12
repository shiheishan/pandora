// Package service 是 audit 模块的业务层：写入、查询与导出。
package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/aegispanel/aegis/internal/module/audit/model"
	"github.com/aegispanel/aegis/internal/module/audit/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"github.com/aegispanel/aegis/internal/pkg/reqmeta"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

const maxDataBytes = 16 << 10

var ipAAD = []byte("audit_events.ip")

// Event.Outcome 的取值；空串即 success。
const (
	OutcomeSuccess = "success"
	OutcomeFailure = "failure"
	OutcomeDenied  = "denied"
	OutcomePartial = "partial"
)

// Event 是一条审计。
type Event struct {
	Action       string // 动作名，如 "user.login"
	ResourceType string
	ResourceID   *uuid.UUID
	// ActorID 只在 ctx 里没有登录主体时用（登录、找回密码等流程）；有主体时一律记主体，
	// 传了与主体不同的 ActorID 直接报错——操作对象放 ResourceID 或 Data。
	ActorID *uuid.UUID
	// Audience 与 ActorID 配套，只在没有主体时用：admin 记为 admin，其余记为 user。
	Audience string
	Outcome  string         // 空串即 success；失败类事件（登录失败、重认证失败）填 failure / denied / partial
	Data     map[string]any // 不放秘密
}

// ActorDirectory 由 identity 实现、app 装配时注入；为 nil 时操作者邮箱留空、搜索框不按邮箱匹配。
type ActorDirectory interface {
	Emails(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)
	IDsByEmail(ctx context.Context, q string, limit int) ([]uuid.UUID, error) // 邮箱包含 q 的用户
}

// ResourceLabeler 按资源类型给出对象可读名（用户邮箱、订单号…），由属主模块注入。
type ResourceLabeler func(ctx context.Context, ids []uuid.UUID) (map[uuid.UUID]string, error)

// Service 提供审计写入与查询。
type Service struct {
	repo   *repo.Repo
	keys   *crypto.Keys
	sealer *crypto.Sealer
	actors ActorDirectory
	labels map[string]ResourceLabeler
}

// New 构造 Service；sealer 来自 keys.Sealer()。
func New(r *repo.Repo, keys *crypto.Keys) (*Service, error) {
	sealer, err := keys.Sealer()
	if err != nil {
		return nil, err
	}
	return &Service{repo: r, keys: keys, sealer: sealer, labels: map[string]ResourceLabeler{}}, nil
}

// UseActorDirectory 只在启动装配时调用（不并发安全）。
func (s *Service) UseActorDirectory(d ActorDirectory) { s.actors = d }

// UseResourceLabeler 只在启动装配时调用（不并发安全）。
func (s *Service) UseResourceLabeler(resourceType string, l ResourceLabeler) {
	s.labels[resourceType] = l
}

// Write 写入一条审计事件。
func (s *Service) Write(ctx context.Context, e Event) error {
	action := strings.TrimSpace(e.Action)
	if action == "" {
		return fmt.Errorf("审计 action 不能为空")
	}

	data := e.Data
	if data == nil {
		data = map[string]any{}
	}
	outcome := e.Outcome
	if outcome == "" {
		outcome = OutcomeSuccess
	}
	if !validOutcome(outcome) {
		return fmt.Errorf("审计 outcome 必须是 success、failure、denied、partial 之一")
	}

	rawJSON, err := marshalData(data)
	if err != nil {
		return err
	}
	if len(rawJSON) > maxDataBytes {
		return fmt.Errorf("审计 data 超过 %d 字节", maxDataBytes)
	}

	p, ok := authn.From(ctx)
	row := model.Event{
		Action:  action,
		Outcome: outcome,
		Data:    string(rawJSON),
	}

	if e.ResourceType != "" {
		row.ResourceType = &e.ResourceType
	}
	row.ResourceID = e.ResourceID

	if err := fillActor(ctx, &row, e, p, ok); err != nil {
		return err
	}

	if rid := reqid.From(ctx); rid != "" {
		row.RequestID = &rid
	}

	m := reqmeta.From(ctx)
	if m.IP != "" {
		row.IPHash = s.keys.HMAC(crypto.InfoHMACIP, []byte(m.IP))
		enc, err := s.sealer.Seal([]byte(m.IP), ipAAD)
		if err != nil {
			plog.From(ctx).Warn("封存审计来源 IP 失败", zap.Error(err))
		} else {
			row.IPEnc = enc
		}
	}
	if m.UserAgent != "" {
		ua := truncateUTF8(cleanText(m.UserAgent), 512)
		row.UserAgent = &ua
	}

	if err := s.repo.Insert(ctx, &row); err != nil {
		return fmt.Errorf("写审计: %w", err)
	}
	return nil
}

func validOutcome(o string) bool {
	switch o {
	case OutcomeSuccess, OutcomeFailure, OutcomeDenied, OutcomePartial:
		return true
	default:
		return false
	}
}

// fillActor 根据 Event 与 ctx 主体填写 actor 相关列。
func fillActor(ctx context.Context, row *model.Event, e Event, p authn.Principal, hasPrincipal bool) error {
	if hasPrincipal && p.UserID != uuid.Nil {
		if e.ActorID != nil && *e.ActorID != p.UserID {
			return fmt.Errorf("审计 ActorID 与登录主体不一致：操作对象应放 ResourceID 或 Data")
		}
		uid := p.UserID
		row.ActorID = &uid
		row.ActorKind, row.ActorAudience, row.AuthContext = actorFromPrincipal(p)
		return nil
	}

	if e.ActorID != nil {
		row.ActorID = e.ActorID
		row.ActorKind = "user"
		switch e.Audience {
		case authn.AudienceAdmin:
			row.ActorKind = "admin"
			fallthrough
		case authn.AudiencePublic:
			aud := e.Audience
			row.ActorAudience = &aud
		case "":
		default:
			return fmt.Errorf("审计 Audience 必须是 public 或 admin")
		}
		return nil
	}

	row.ActorID = nil
	if reqmeta.From(ctx).IP != "" {
		row.ActorKind = "anonymous"
	} else {
		row.ActorKind = "system"
	}
	return nil
}

func actorFromPrincipal(p authn.Principal) (kind string, audience *string, authCtx *string) {
	switch p.Audience {
	case authn.AudienceAdmin:
		kind = "admin"
	case authn.AudiencePublic:
		kind = "user"
	default:
		return "user", nil, nil
	}
	audience = &p.Audience
	if p.SessionID != "" {
		c := "session"
		if p.RecentlyReauthed(time.Now()) {
			c = "reauth"
		}
		authCtx = &c
	}
	return kind, audience, authCtx
}

// marshalData 序列化 data，并去掉 PG jsonb 不收的 \u0000。
// 非法 UTF-8 由 encoding/json 换成 U+FFFD；NUL 只在出现时走一次解码、清洗、再编码。
func marshalData(data map[string]any) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("序列化审计 data: %w", err)
	}
	if !bytes.Contains(raw, []byte(`\u0000`)) {
		return raw, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("清洗审计 data: %w", err)
	}
	raw, err = json.Marshal(stripNUL(v))
	if err != nil {
		return nil, fmt.Errorf("序列化审计 data: %w", err)
	}
	return raw, nil
}

func stripNUL(v any) any {
	switch x := v.(type) {
	case string:
		return strings.ReplaceAll(x, "\x00", "")
	case []any:
		for i := range x {
			x[i] = stripNUL(x[i])
		}
		return x
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			out[strings.ReplaceAll(k, "\x00", "")] = stripNUL(val)
		}
		return out
	default:
		return v
	}
}

// cleanText 把来自请求的文本变成 PG text 能收的样子：去 NUL、非法 UTF-8 换成 U+FFFD。
func cleanText(s string) string {
	return strings.ToValidUTF8(strings.ReplaceAll(s, "\x00", ""), "\uFFFD")
}

func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	b := s[:maxBytes]
	for len(b) > 0 && !utf8.ValidString(b) {
		b = b[:len(b)-1]
	}
	return b
}
