package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
)

// TemplateBroadcast 是后台群发外壳模板。
const TemplateBroadcast = "admin.broadcast"

const (
	categoryMarketing = "marketing"
	bulkMailMax       = 20000
)

// init 把外壳模板追加进登记表（registry.go 不在本块范围，以后挪进 registry.go）。
func init() {
	templateDefs = append(templateDefs, templateDef{
		Code:        TemplateBroadcast,
		Channel:     ChannelEmail,
		Category:    categoryMarketing,
		Description: "后台「批量运营 → 群发邮件」：主题与正文由管理员每次填写，经变量 subject、body 传入；关掉营销邮件的用户不发",
		Allowed:     []string{"subject", "body"},
		Subject:     "{{subject}}",
		Body:        "{{body}}",
	})
}

// BulkMailInput 是后台群发邮件的输入。
type BulkMailInput struct {
	Filter  identitysvc.BulkFilter
	Subject string
	Body    string
}

// BulkMailResult 是群发排队的结果：Queued 为写入投递表的封数，Skipped 为因无邮箱或关掉营销邮件而跳过的人数。
// 发送是异步的，返回的是排了多少封，不是 SMTP 已送达多少封。
type BulkMailResult struct {
	Queued  int
	Skipped int
}

// SendBulkMail 按筛选条件给命中用户各排一封营销邮件投递；关掉营销邮件或无邮箱的用户跳过。
func (s *Service) SendBulkMail(ctx context.Context, in BulkMailInput) (BulkMailResult, error) {
	subject := strings.TrimSpace(in.Subject)
	body := strings.TrimSpace(in.Body)
	fields := map[string]string{}
	if strings.ContainsRune(subject, '\x00') || utf8.RuneCountInString(subject) < 1 || utf8.RuneCountInString(subject) > 200 {
		fields["subject"] = "主题必填，不超过 200 字"
	}
	if strings.ContainsRune(body, '\x00') || utf8.RuneCountInString(body) < 1 || utf8.RuneCountInString(body) > 20000 {
		fields["body"] = "正文必填，不超过 20000 字"
	}
	if len(fields) > 0 {
		return BulkMailResult{}, errs.Invalid(fields)
	}
	if s.dir == nil {
		return BulkMailResult{}, errs.Wrap(errs.Internal, errors.New("notify: Directory 未装配"))
	}

	recips, total, err := s.dir.BulkRecipients(ctx, in.Filter, bulkMailMax)
	if err != nil {
		return BulkMailResult{}, err
	}
	if total == 0 {
		return BulkMailResult{}, errs.Newf(errs.ValidationFailed, "这个筛选条件下没有用户，先用预览确认一下")
	}
	if total > int64(bulkMailMax) {
		return BulkMailResult{}, errs.Newf(errs.ValidationFailed, "一次最多群发 20000 人，请缩小范围分批发送")
	}

	site, err := s.settings.Site(ctx)
	if err != nil {
		return BulkMailResult{}, err
	}
	loc, err := time.LoadLocation(site.Timezone)
	if err != nil {
		loc = time.UTC
	}

	userIDs := make([]uuid.UUID, len(recips))
	for i, r := range recips {
		userIDs[i] = r.UserID
	}
	optOut, err := s.store.MarketingEmailOptOuts(ctx, userIDs)
	if err != nil {
		return BulkMailResult{}, err
	}

	var deliveries []model.Delivery
	var queued, skipped int
	for _, r := range recips {
		email := strings.TrimSpace(r.Email)
		if email == "" || optOut[r.UserID] {
			skipped++
			continue
		}
		expire := ""
		if r.PeriodEnd != nil {
			expire = r.PeriodEnd.In(loc).Format("2006-01-02")
		}
		bodyOut := strings.NewReplacer(
			"$email", email,
			"$plan", r.PlanName,
			"$expire", expire,
		).Replace(body)

		id, err := uuid.NewV7()
		if err != nil {
			return BulkMailResult{}, errs.Wrap(errs.Internal, err)
		}
		raw, err := json.Marshal(deliveryPayload{
			To:   email,
			Vars: map[string]string{"subject": subject, "body": bodyOut},
		})
		if err != nil {
			return BulkMailResult{}, errs.Wrap(errs.Internal, err)
		}
		blob, err := s.sealer.Seal(raw, deliveryAAD(id))
		if err != nil {
			return BulkMailResult{}, errs.Wrap(errs.Internal, err)
		}
		uid := r.UserID
		deliveries = append(deliveries, model.Delivery{
			ID:            id,
			UserID:        &uid,
			TemplateCode:  TemplateBroadcast,
			Channel:       ChannelEmail,
			RecipientHash: s.recipientHash(email),
			PayloadEnc:    blob,
			Status:        model.StatusQueued,
			MaxAttempts:   maxAttempts,
		})
		queued++
	}

	batchID, err := uuid.NewV7()
	if err != nil {
		return BulkMailResult{}, errs.Wrap(errs.Internal, err)
	}
	batch := batchID.String()

	err = s.tx.Do(ctx, func(ctx context.Context) error {
		if err := s.store.InsertDeliveries(ctx, deliveries); err != nil {
			return err
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "user.bulk_mail_sent",
			ResourceType: "notification_delivery",
			Data: map[string]any{
				"batch":                 batch,
				"matched":               total,
				"queued":                queued,
				"skipped_by_preference": skipped,
				"subject":               subject,
				"filter":                identitysvc.BulkFilterDigest(in.Filter),
			},
		}); err != nil {
			return err
		}
		if queued > 0 {
			tx.AfterCommit(ctx, TaskDispatch, s.kick)
		}
		return nil
	})
	if err != nil {
		return BulkMailResult{}, err
	}
	return BulkMailResult{Queued: queued, Skipped: skipped}, nil
}
