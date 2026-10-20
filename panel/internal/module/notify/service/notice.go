package service

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// noticeContact 是写一条通知要的收件信息（批量预取）。
type noticeContact struct {
	Email  string          // 空 = 不发邮件
	ChatID int64           // 0 = 没绑 Telegram
	Prefs  map[string]bool // 用户改过的偏好，键 category + "/" + channel
}

// noticeEnv 是一轮写通知共用的环境（一次读好）。
type noticeEnv struct {
	Site      string                    // 站点名，填邮件模板的 {{site}}
	Templates map[string]model.Template // 库里的模板，键 code + "/" + channel
	Telegram  bool                      // Telegram 当前可发（s.telegramReady）
}

// loadNoticeEnv 读站点名、全部模板（s.store.ListTemplates）、Telegram 是否可发。
func (s *Service) loadNoticeEnv(ctx context.Context) (noticeEnv, error) {
	siteRow, err := s.settings.Site(ctx)
	if err != nil {
		return noticeEnv{}, err
	}
	rows, err := s.store.ListTemplates(ctx)
	if err != nil {
		return noticeEnv{}, err
	}
	tpl := make(map[string]model.Template, len(rows))
	for _, r := range rows {
		tpl[r.Code+"/"+r.Channel] = r
	}
	_, tgOK, err := s.telegramReady(ctx)
	if err != nil {
		return noticeEnv{}, err
	}
	return noticeEnv{Site: siteRow.Name, Templates: tpl, Telegram: tgOK}, nil
}

// loadContacts 批量读一批用户的邮箱（s.dir.Emails）、Telegram 绑定（s.store.BindingsForUsers）与偏好（s.store.PreferencesForUsers），每类一条 SQL。
func (s *Service) loadContacts(ctx context.Context, userIDs []uuid.UUID) (map[uuid.UUID]noticeContact, error) {
	if len(userIDs) == 0 {
		return map[uuid.UUID]noticeContact{}, nil
	}
	emails, err := s.dir.Emails(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	bindings, err := s.store.BindingsForUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	chatByUser := map[uuid.UUID]int64{}
	for _, b := range bindings {
		chatByUser[b.UserID] = b.ChatID
	}
	prefsRows, err := s.store.PreferencesForUsers(ctx, userIDs)
	if err != nil {
		return nil, err
	}
	prefsByUser := map[uuid.UUID]map[string]bool{}
	for _, p := range prefsRows {
		m := prefsByUser[p.UserID]
		if m == nil {
			m = map[string]bool{}
			prefsByUser[p.UserID] = m
		}
		m[p.Category+"/"+p.Channel] = p.Enabled
	}
	out := make(map[uuid.UUID]noticeContact, len(userIDs))
	for _, id := range userIDs {
		out[id] = noticeContact{
			Email:  emails[id],
			ChatID: chatByUser[id],
			Prefs:  prefsByUser[id],
		}
	}
	return out, nil
}

func defsForCode(code string) []templateDef {
	var out []templateDef
	for _, d := range templateDefs {
		if d.Code == code {
			out = append(out, d)
		}
	}
	return out
}

func noticeFilterVars(def templateDef, vars map[string]string, site string) map[string]string {
	out := make(map[string]string)
	for _, k := range def.Allowed {
		if v, ok := vars[k]; ok && !strings.ContainsRune(v, '\x00') {
			out[k] = v
		}
	}
	if containsAllowed(def.Allowed, "site") && !payloadVarsHas(out, "site") && site != "" {
		out["site"] = site
	}
	return out
}

func prefDisabled(prefs map[string]bool, category, channel string) bool {
	if prefs == nil {
		return false
	}
	v, ok := prefs[category+"/"+channel]
	return ok && !v
}

func (s *Service) queueDelivery(ctx context.Context, userID uuid.UUID, code, channel, to string, vars map[string]string) error {
	id, err := uuid.NewV7()
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	raw, err := json.Marshal(deliveryPayload{To: to, Vars: vars})
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	blob, err := s.sealer.Seal(raw, deliveryAAD(id))
	if err != nil {
		return errs.Wrap(errs.Internal, err)
	}
	uid := userID
	if err := s.store.InsertDelivery(ctx, &model.Delivery{
		ID:            id,
		UserID:        &uid,
		TemplateCode:  code,
		Channel:       channel,
		RecipientHash: s.recipientHash(to),
		PayloadEnc:    blob,
		Status:        model.StatusQueued,
		MaxAttempts:   maxAttempts,
	}); err != nil {
		return err
	}
	tx.AfterCommit(ctx, TaskDispatch, s.kick)
	return nil
}

// writeNotice 在 ctx 的事务里给一个用户写一件事的通知：先插去重键（撞了返回 false, nil，什么都不写），
// 再按 code 在 templateDefs 里登记的各渠道写：站内信写 notification_inbox；邮件、Telegram 写一条 queued 投递并在提交后入队派发。
// 返回是否新写。
func (s *Service) writeNotice(ctx context.Context, env noticeEnv, userID uuid.UUID, c noticeContact, code string, vars map[string]string, dedupeKey string) (bool, error) {
	log := plog.From(ctx)
	if dedupeKey == "" || len(dedupeKey) > 200 {
		return false, errs.Wrap(errs.Internal, fmt.Errorf("notify: 去重键无效"))
	}
	defs := defsForCode(code)
	if len(defs) == 0 {
		return false, errs.Wrap(errs.Internal, fmt.Errorf("notify: 未登记的通知模板 %q", code))
	}

	inserted, err := s.store.InsertEvent(ctx, &model.Event{
		DedupeKey:    dedupeKey,
		UserID:       &userID,
		TemplateCode: code,
	})
	if err != nil {
		return false, err
	}
	if !inserted {
		return false, nil
	}

	for _, def := range defs {
		filtered := noticeFilterVars(def, vars, env.Site)
		tplKey := def.Code + "/" + def.Channel
		tpl, ok := env.Templates[tplKey]
		if !ok {
			log.Warn("notify: 模板缺失，跳过渠道", zap.String("template", tplKey))
			continue
		}

		switch def.Channel {
		case ChannelInApp:
			sub, err := renderText(tpl.Subject, def.Allowed, filtered)
			if err != nil {
				log.Warn("notify: 站内信主题渲染失败，跳过", zap.String("template", tplKey), zap.Error(err))
				continue
			}
			body, err := renderText(tpl.Body, def.Allowed, filtered)
			if err != nil {
				log.Warn("notify: 站内信正文渲染失败，跳过", zap.String("template", tplKey), zap.Error(err))
				continue
			}
			sub = truncateRunes(headerSafe(sub), 200)
			body = truncateRunes(strings.TrimSpace(body), 20000)
			if sub == "" || body == "" {
				continue
			}
			if err := s.store.InsertInbox(ctx, &model.InboxItem{
				UserID:       userID,
				TemplateCode: code,
				Subject:      sub,
				Body:         body,
			}); err != nil {
				return false, err
			}

		case ChannelEmail:
			if def.Category != categoryTransactional && prefDisabled(c.Prefs, def.Category, ChannelEmail) {
				continue
			}
			if strings.TrimSpace(c.Email) == "" {
				continue
			}
			if err := s.queueDelivery(ctx, userID, code, ChannelEmail, c.Email, filtered); err != nil {
				return false, err
			}

		case ChannelTelegram:
			if !env.Telegram || c.ChatID == 0 {
				continue
			}
			if def.Category != categoryTransactional && prefDisabled(c.Prefs, def.Category, ChannelTelegram) {
				continue
			}
			to := strconv.FormatInt(c.ChatID, 10)
			if err := s.queueDelivery(ctx, userID, code, ChannelTelegram, to, filtered); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// NotifyUser 给用户发一件事的通知（别的模块用，如 support 工单回复）。在调用方事务里执行（s.tx.Do 加入），
// dedupeKey 必填（同一件事只通知一次）；code 必须在登记表里，否则返回内部错误。
func (s *Service) NotifyUser(ctx context.Context, userID uuid.UUID, code string, vars map[string]string, dedupeKey string) error {
	if s.dir == nil {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: Directory 未装配"))
	}
	return s.tx.Do(ctx, func(ctx context.Context) error {
		env, err := s.loadNoticeEnv(ctx)
		if err != nil {
			return err
		}
		contacts, err := s.loadContacts(ctx, []uuid.UUID{userID})
		if err != nil {
			return err
		}
		c := contacts[userID]
		_, err = s.writeNotice(ctx, env, userID, c, code, vars, dedupeKey)
		return err
	})
}
