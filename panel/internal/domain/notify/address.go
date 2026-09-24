// [INPUT]: 依赖 notify.go 的 Service（模板表、投递表、收件人哈希），依赖 pgx 的业务事务，依赖 domain/appearance 的 SiteNameTx
// [OUTPUT]: 对外提供 EnqueueToAddress、Kick；包内提供 recipientPayloadKey、scrubAddressPayloadSQL、withSite
// [POS]: domain/notify 的「按地址投递」分支：收件人还不是用户（注册验证码）时走这里，派发仍由 notify.go 的 Dispatch/deliver 统一完成
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package notify

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
)

// recipientPayloadKey 是按地址投递时存放收件地址的保留键。
//
// 普通投递的收件人由 user_id 反查，投递表里只留哈希；按地址投递没有
// user_id，地址只能随行存着。它不参与渲染（deliver 取出后即删），
// 并在投递到达终态时连同整个 payload 一起清空，见 scrubAddressPayloadSQL。
const recipientPayloadKey = "_recipient"

// scrubAddressPayloadSQL 用在投递转入终态（sent / suppressed / 最终 failed）
// 的 UPDATE 里：按地址投递的 payload 带着收件邮箱和验证码明文，
// 发完就没有理由再留着。普通投递的 payload 不动 —— 站内信要靠它展示。
const scrubAddressPayloadSQL = `payload = CASE WHEN user_id IS NULL THEN '{}'::jsonb ELSE payload END`

// EnqueueToAddress 给一个还不是用户的邮箱排一封信。vars 里没给 site 时按生效主题的站点名填。
//
// 目前唯一的调用方是注册验证码：用户在第 2 步之前根本不存在，Enqueue 的
// 「按 user_id 反查邮箱」走不通。这里只排邮件渠道，也不查退订偏好 ——
// 没有用户就没有偏好，能走这条路的模板都应当是 transactional。
//
// dedupeKey 必填：它是投递表的唯一键，同一件事重试只会排一次。
func (s *Service) EnqueueToAddress(ctx context.Context, tx pgx.Tx, tenantID,
	code, address string, vars map[string]string, dedupeKey string) error {

	if address == "" || dedupeKey == "" {
		return fmt.Errorf("按地址排队 %s：收件地址与去重键都不能为空", code)
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM notification_templates
		                WHERE tenant_id = $1 AND code = $2 AND channel = $3
		                  AND status = 'active' AND locale = 'zh-CN')`,
		tenantID, code, string(ChannelEmail)).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		// 与 Enqueue 同样不当作错误：模板被停用时业务本身不该失败。
		s.log.Warn("通知模板缺失", "code", code, "channel", ChannelEmail)
		return nil
	}

	payload, err := withSite(ctx, tx, tenantID, vars)
	if err != nil {
		return err
	}
	payload[recipientPayloadKey] = address

	if _, err := tx.Exec(ctx, `
		INSERT INTO notification_deliveries
			(tenant_id, user_id, template_code, channel, dedupe_key,
			 recipient_hash, payload, status, max_attempts, next_retry_at)
		VALUES ($1, NULL, $2, $3, $4, $5, $6, 'queued', 5, now())
		ON CONFLICT DO NOTHING`,
		tenantID, code, string(ChannelEmail), dedupeKey+":"+string(ChannelEmail),
		s.hash(address), payload); err != nil {
		return fmt.Errorf("按地址排队 %s: %w", code, err)
	}
	return nil
}

// withSite 把入队变量转成 payload，并补上 {{site}}。
//
// 站点名以生效主题的 branding.site_name 为准（appearance.SiteNameTx），
// 在入队这一处统一补，调用方不必各自去查；显式传了 site 的以调用方为准。
func withSite(ctx context.Context, tx pgx.Tx, tenantID string, vars map[string]string) (map[string]any, error) {
	payload := make(map[string]any, len(vars)+2)
	for k, v := range vars {
		payload[k] = v
	}
	if _, ok := payload["site"]; !ok {
		site, err := appearance.SiteNameTx(ctx, tx, tenantID)
		if err != nil {
			return nil, err
		}
		payload["site"] = site
	}
	return payload, nil
}

// Kick 让派发循环立刻跑一轮，不等下一个周期。
//
// 定时派发是 5 分钟一轮，而验证码 10 分钟就过期：排进去的验证码可能要
// 等半个有效期才发出。调用方在业务事务提交之后调它；不阻塞，循环正忙时
// 多次 Kick 合并成一次。循环没有启动（StartScanner 没调）时它什么也不做。
func (s *Service) Kick() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
