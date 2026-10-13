package service

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/plugin/model"
	"github.com/aegispanel/aegis/internal/module/plugin/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

var hookCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,38}$`)

// List 返回全部钩子及近 7 天投递统计。
func (s *Service) List(ctx context.Context) ([]Hook, error) {
	hooks, err := s.store.ListHooks(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := s.store.HookStats(ctx, s.now().Add(-statsWindow))
	if err != nil {
		return nil, err
	}
	statByID := make(map[uuid.UUID]repo.HookStat, len(stats))
	for _, st := range stats {
		statByID[st.HookID] = st
	}
	loc := displayLoc
	out := make([]Hook, 0, len(hooks))
	for _, h := range hooks {
		var events []string
		if err := json.Unmarshal([]byte(h.Events), &events); err != nil {
			plog.From(ctx).Warn("plugin: 钩子事件 JSON 解析失败", zap.String("code", h.Code), zap.Error(err))
			events = []string{}
		}
		if events == nil {
			events = []string{}
		}
		st := statByID[h.ID]
		lastSent := ""
		if st.LastSentAt != nil {
			lastSent = st.LastSentAt.In(loc).Format("2006-01-02 15:04")
		}
		out = append(out, Hook{
			ID:          h.ID,
			Code:        h.Code,
			Name:        h.Name,
			Description: h.Description,
			Enabled:     h.Enabled,
			Events:      events,
			EndpointURL: h.EndpointURL,
			HasSecret:   len(h.SecretEnc) > 0,
			TimeoutMS:   h.TimeoutMS,
			MaxAttempts: h.MaxAttempts,
			QueuedCount: int(st.Queued),
			FailedCount: int(st.Failed),
			SentCount7d: int(st.Sent),
			LastSentAt:  lastSent,
		})
	}
	return out, nil
}

func dedupeEvents(events []string) []string {
	if events == nil {
		return []string{}
	}
	seen := make(map[string]bool, len(events))
	out := make([]string, 0, len(events))
	for _, e := range events {
		if seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	return out
}

func hookFieldNul(fields map[string]string, key, value string) {
	if strings.Contains(value, "\x00") {
		fields[key] = "不能包含空字符"
	}
}

// SaveHook 按 code 新建或修改钩子，返回新建且未指定密钥时生成的密钥。
func (s *Service) SaveHook(ctx context.Context, in SaveHookInput) (string, error) {
	code := strings.ToLower(strings.TrimSpace(in.Code))
	name := strings.TrimSpace(in.Name)
	description := strings.TrimSpace(in.Description)
	endpointURL := strings.TrimSpace(in.EndpointURL)
	secret := strings.TrimSpace(in.Secret)
	events := dedupeEvents(in.Events)

	fields := map[string]string{}
	if code == "" {
		fields["code"] = "插件标识必填，小写字母开头"
	} else if !hookCodePattern.MatchString(code) {
		fields["code"] = "插件标识只能用小写字母、数字、- 和 _，以小写字母开头，2–39 个字符"
	}
	if name == "" {
		fields["name"] = "插件名称必填"
	} else if utf8.RuneCountInString(name) > 80 {
		fields["name"] = "插件名称最多 80 个字"
	}
	if utf8.RuneCountInString(description) > 500 {
		fields["description"] = "说明最多 500 个字"
	}
	if len(endpointURL) > 2048 {
		fields["endpoint_url"] = "地址太长"
	} else if err := validateEndpoint(ctx, endpointURL, s.opts.AllowPrivate); err != nil {
		fields["endpoint_url"] = err.Error()
	}
	for _, e := range events {
		if !KnownEvent(e) {
			fields["events"] = "未知事件：" + e
			break
		}
	}
	if in.Enabled && len(events) == 0 {
		fields["events"] = "启用前至少要订阅一个事件，否则这个钩子永远不会触发"
	}
	timeoutMS := in.TimeoutMS
	if timeoutMS == 0 {
		timeoutMS = defaultTimeoutMS
	}
	if timeoutMS < 500 || timeoutMS > 30000 {
		fields["timeout_ms"] = "超时必须在 500–30000 毫秒之间"
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = defaultMaxAttempts
	}
	if maxAttempts < 1 || maxAttempts > 10 {
		fields["max_attempts"] = "最多尝试次数必须在 1–10 之间"
	}
	if len(secret) > 256 {
		fields["secret"] = "签名密钥最多 256 个字符"
	}
	hookFieldNul(fields, "code", code)
	hookFieldNul(fields, "name", name)
	hookFieldNul(fields, "description", description)
	hookFieldNul(fields, "endpoint_url", endpointURL)
	hookFieldNul(fields, "secret", secret)
	for _, e := range events {
		hookFieldNul(fields, "events", e)
	}
	if len(fields) > 0 {
		return "", errs.Invalid(fields)
	}

	eventsJSON, _ := json.Marshal(events)
	p, hasPrincipal := authn.From(ctx)

	var generated string
	var id uuid.UUID
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		existing, found, err := s.store.HookByCode(ctx, code, true)
		if err != nil {
			return err
		}
		secretWork := secret
		var sealed []byte
		if !found && secretWork == "" {
			b := make([]byte, 32)
			if _, err := rand.Read(b); err != nil {
				return errs.Wrap(errs.Internal, err)
			}
			secretWork = "whsec_" + base64.RawURLEncoding.EncodeToString(b)
			generated = secretWork
		}
		if secretWork != "" {
			sealed, err = s.sealer.Seal([]byte(secretWork), secretAAD(code))
			if err != nil {
				return errs.Wrap(errs.Internal, err)
			}
		}
		if found {
			existing.Name = name
			existing.Description = description
			existing.Enabled = in.Enabled
			existing.Events = string(eventsJSON)
			existing.EndpointURL = endpointURL
			existing.TimeoutMS = timeoutMS
			existing.MaxAttempts = maxAttempts
			if secretWork != "" {
				existing.SecretEnc = sealed
			}
			if err := s.store.UpdateHook(ctx, &existing, secretWork != ""); err != nil {
				return err
			}
			id = existing.ID
		} else {
			h := &model.Hook{
				Code:        code,
				Name:        name,
				Description: description,
				Enabled:     in.Enabled,
				Events:      string(eventsJSON),
				EndpointURL: endpointURL,
				SecretEnc:   sealed,
				TimeoutMS:   timeoutMS,
				MaxAttempts: maxAttempts,
			}
			if hasPrincipal {
				h.CreatedBy = &p.UserID
			}
			if err := s.store.InsertHook(ctx, h); err != nil {
				if errors.Is(err, repo.ErrDuplicate) {
					return errs.Newf(errs.Conflict, "该插件标识已存在")
				}
				return err
			}
			id = h.ID
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionHookSave,
			ResourceType: ResourceHook,
			ResourceID:   &id,
			Data: map[string]any{
				"code":           code,
				"enabled":        in.Enabled,
				"events":         events,
				"endpoint_host":  hostOf(endpointURL),
				"created":        !found,
				"secret_changed": secretWork != "", // 含新建时自动生成
			},
		})
	})
	if err != nil {
		return "", err
	}
	return generated, nil
}

// DeleteHook 按 code 删除钩子。
func (s *Service) DeleteHook(ctx context.Context, code string) error {
	code = strings.ToLower(strings.TrimSpace(code))
	return s.tx.Do(ctx, func(ctx context.Context) error {
		h, found, err := s.store.HookByCode(ctx, code, true)
		if err != nil {
			return err
		}
		if !found {
			return errs.Newf(errs.NotFound, "插件不存在")
		}
		if err := s.store.DeleteHook(ctx, h.ID); err != nil {
			return err
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionHookDelete,
			ResourceType: ResourceHook,
			ResourceID:   &h.ID,
			Data: map[string]any{
				"code":          h.Code,
				"endpoint_host": hostOf(h.EndpointURL),
			},
		})
	})
}

// Deliveries 返回某钩子最近的投递记录；钩子不存在时返回空列表。
func (s *Service) Deliveries(ctx context.Context, code string, limit int) ([]Delivery, error) {
	if limit <= 0 || limit > maxDeliveries {
		limit = defaultDeliveries
	}
	code = strings.ToLower(strings.TrimSpace(code))
	h, found, err := s.store.HookByCode(ctx, code, false)
	if err != nil {
		return nil, err
	}
	if !found {
		return []Delivery{}, nil
	}
	rows, err := s.store.DeliveriesByHook(ctx, h.ID, limit)
	if err != nil {
		return nil, err
	}
	loc := displayLoc
	out := make([]Delivery, 0, len(rows))
	for _, d := range rows {
		rc := 0
		if d.ResponseCode != nil {
			rc = *d.ResponseCode
		}
		sentAt := ""
		if d.SentAt != nil {
			sentAt = d.SentAt.In(loc).Format("2006-01-02 15:04:05")
		}
		out = append(out, Delivery{
			Event:        d.Event,
			Status:       d.Status,
			Attempts:     d.Attempts,
			ResponseCode: rc,
			ErrorMessage: d.ErrorMessage,
			CreatedAt:    d.CreatedAt.In(loc).Format("2006-01-02 15:04:05"),
			SentAt:       sentAt,
			DurationMS:   d.LastDurationMS,
		})
	}
	return out, nil
}

// TestHook 立刻往钩子发一条测试事件，同步返回状态码与耗时；测试投递不落库。
func (s *Service) TestHook(ctx context.Context, code string) (TestResult, error) {
	code = strings.ToLower(strings.TrimSpace(code))
	h, found, err := s.store.HookByCode(ctx, code, false)
	if err != nil {
		return TestResult{}, err
	}
	if !found {
		return TestResult{}, errs.Newf(errs.NotFound, "插件不存在")
	}
	if err := s.tx.Do(ctx, func(ctx context.Context) error {
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       ActionTestSent,
			ResourceType: ResourceTest,
			Data: map[string]any{
				"kind":          testKind,
				"target":        h.Code,
				"endpoint_host": hostOf(h.EndpointURL),
			},
		})
	}); err != nil {
		return TestResult{}, err
	}
	plain, err := s.sealer.Open(h.SecretEnc, secretAAD(h.Code))
	if err != nil {
		return TestResult{}, errs.Wrap(errs.Internal, err)
	}
	if _, err := parseEndpoint(h.EndpointURL, s.opts.AllowPrivate); err != nil {
		return TestResult{}, errs.Newf(errs.ValidationFailed, "发送失败："+err.Error())
	}
	body, err := json.Marshal(struct {
		Event   string `json:"event"`
		Message string `json:"message"`
		SentAt  string `json:"sent_at"`
	}{
		Event:   "panel.test",
		Message: "来自潘多拉面板的测试事件",
		SentAt:  s.now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return TestResult{}, errs.Wrap(errs.Internal, err)
	}
	respCode, dur, err := postSigned(ctx, s.client, h.EndpointURL, "panel.test", "test-"+h.ID.String(),
		body, string(plain), time.Duration(h.TimeoutMS)*time.Millisecond, s.now())
	if err != nil {
		return TestResult{}, errs.Newf(errs.ValidationFailed, "发送失败："+err.Error())
	}
	if respCode < 200 || respCode >= 300 {
		return TestResult{}, errs.Newf(errs.ValidationFailed, fmt.Sprintf("对方返回 HTTP %d，不是 2xx", respCode))
	}
	ms := 0
	if dur != nil {
		ms = *dur
	}
	return TestResult{ResponseCode: respCode, DurationMS: ms}, nil
}
