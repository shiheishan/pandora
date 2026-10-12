package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/module/notify/model"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/jobs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/tx"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

var _ identitysvc.VerificationMailer = (*Service)(nil)

// deliveryPayload 是封存在 payload_enc 里的内容。
type deliveryPayload struct {
	To   string            `json:"to"`
	Vars map[string]string `json:"vars"`
}

// deliveryAAD 是封存 payload 的附加数据，绑定到投递 id，防止密文被挪到别的行。
func deliveryAAD(id uuid.UUID) []byte {
	return []byte("notification_deliveries.payload:" + id.String())
}

// recipientHash 是收件地址的 HMAC（小写后计算）。
func (s *Service) recipientHash(address string) []byte {
	return s.keys.HMAC(crypto.InfoHMACNotify, []byte(strings.ToLower(address)))
}

// SendToAddress 实现 identity 的 VerificationMailer：在调用方事务里写一条 queued 投递，提交后入队派发。
// 模板缺失时只记日志不报错（业务不因模板被删而失败）；SMTP 未配置、notify.email 关闭都照写记录。
func (s *Service) SendToAddress(ctx context.Context, templateCode, address string, vars map[string]string, userID *uuid.UUID) error {
	address = strings.TrimSpace(address)
	if err := checkBareAddress(address); err != nil {
		return errs.Invalid(map[string]string{"email": "邮箱格式不正确"})
	}
	def, ok := lookupDef(templateCode, ChannelEmail)
	if !ok {
		return errs.Wrap(errs.Internal, fmt.Errorf("notify: 未登记的邮件模板 %q", templateCode))
	}

	payloadVars := make(map[string]string)
	for _, k := range def.Allowed {
		if v, ok := vars[k]; ok && !strings.ContainsRune(v, '\x00') {
			payloadVars[k] = v
		}
	}
	if containsAllowed(def.Allowed, "site") && !payloadVarsHas(payloadVars, "site") {
		site, err := s.settings.Site(ctx)
		if err != nil {
			return err
		}
		payloadVars["site"] = site.Name
	}

	return s.tx.Do(ctx, func(ctx context.Context) error {
		_, ok, err := s.store.GetTemplate(ctx, templateCode, ChannelEmail)
		if err != nil {
			return err
		}
		if !ok {
			plog.From(ctx).Warn("notify: 模板缺失，不发送", zap.String("template", templateCode))
			return nil
		}

		id, err := uuid.NewV7()
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		raw, err := json.Marshal(deliveryPayload{To: address, Vars: payloadVars})
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}
		blob, err := s.sealer.Seal(raw, deliveryAAD(id))
		if err != nil {
			return errs.Wrap(errs.Internal, err)
		}

		if err := s.store.InsertDelivery(ctx, &model.Delivery{
			ID:            id,
			UserID:        userID,
			TemplateCode:  templateCode,
			Channel:       ChannelEmail,
			RecipientHash: s.recipientHash(address),
			PayloadEnc:    blob,
			Status:        model.StatusQueued,
			MaxAttempts:   maxAttempts,
		}); err != nil {
			return err
		}

		tx.AfterCommit(ctx, TaskDispatch, s.kick)
		return nil
	})
}

func containsAllowed(allowed []string, name string) bool {
	for _, k := range allowed {
		if k == name {
			return true
		}
	}
	return false
}

func payloadVarsHas(m map[string]string, key string) bool {
	_, ok := m[key]
	return ok
}

// kick 入队一次 notify:dispatch（TaskID 按秒分桶去重）；失败只记日志，周期任务会补发。
func (s *Service) kick(ctx context.Context) {
	if s.queue == nil {
		return
	}
	task, err := jobs.NewTask(ctx, TaskDispatch, nil)
	if err != nil {
		plog.From(ctx).Warn("notify: 入队投递任务失败，等周期任务补发", zap.Error(err))
		return
	}
	_, err = s.queue.EnqueueContext(ctx, task,
		asynq.TaskID(fmt.Sprintf("%s:%d", TaskDispatch, s.now().Unix())),
		asynq.Queue("default"),
		asynq.MaxRetry(0),
		asynq.Timeout(DispatchTimeout),
	)
	if err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			return
		}
		plog.From(ctx).Warn("notify: 入队投递任务失败，等周期任务补发", zap.Error(err))
	}
}
