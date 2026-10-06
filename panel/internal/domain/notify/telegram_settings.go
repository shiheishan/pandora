// [INPUT]: 依赖 platform 的 db 租户事务与 crypto 信封（Bot Token 加密）；读写 system_settings 的 telegram.* 键
// [OUTPUT]: 对外提供 Service.TelegramAdminChat、Service.SaveTelegramSettings 与入参 TelegramSettingsInput
// [POS]: domain/notify 的 Telegram 渠道后台配置存取（从 api/admin/telegram.go 下沉）：与 telegram.go 的 LoadTelegramConfig 读同一组键；请求校验与发送器缓存失效留在 handler

package notify

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// TelegramAdminChat 读管理员群组 chat id（system_settings 的 telegram.admin_chat_id），缺行回 nil。
// 目前只作测试发送的默认目标；推送管理告警见待决 D-A-4，未定前不做。
func (s *Service) TelegramAdminChat(ctx context.Context, tenantID string) (*int64, error) {
	var chat *int64
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT (value #>> '{}')::bigint FROM system_settings
			 WHERE tenant_id = $1 AND key = 'telegram.admin_chat_id'`, tenantID).Scan(&chat)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	return chat, err
}

// TelegramSettingsInput 是已校验过的 Telegram 配置写入。
type TelegramSettingsInput struct {
	TenantID    string
	ActorID     string
	Enabled     bool
	BotUsername string
	// BotToken 为空表示不修改
	BotToken string
	// SetAdminChat 为 false 时不动 telegram.admin_chat_id；为 true 时写 AdminChat（nil 即清空）
	SetAdminChat bool
	AdminChat    *int64
	// Envelope 把 Bot Token 密封后再落库
	Envelope *crypto.Envelope
}

// SaveTelegramSettings 在一个事务里 upsert 全部 telegram.* 键。
func (s *Service) SaveTelegramSettings(ctx context.Context, in TelegramSettingsInput) error {
	tenantID := in.TenantID
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID},
		func(tx pgx.Tx) error {
			set := func(key string, val any) error {
				raw, err := json.Marshal(val)
				if err != nil {
					return err
				}
				_, err = tx.Exec(ctx, `
					INSERT INTO system_settings (tenant_id, key, value)
					VALUES ($1,$2,$3::jsonb)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET value = EXCLUDED.value, updated_at = now()`,
					tenantID, key, string(raw))
				return err
			}
			if err := set("telegram.enabled", in.Enabled); err != nil {
				return err
			}
			if err := set("telegram.bot_username", in.BotUsername); err != nil {
				return err
			}
			if in.SetAdminChat {
				if err := set("telegram.admin_chat_id", in.AdminChat); err != nil {
					return err
				}
			}
			// 空 token 表示「不修改」—— 界面上不回显已有 token，
			// 提交时留空就该保留原值，而不是把它清掉。
			if in.BotToken != "" {
				sealed, err := in.Envelope.Seal([]byte(in.BotToken), []byte("telegram"))
				if err != nil {
					return err
				}
				if _, err := tx.Exec(ctx, `
					INSERT INTO system_settings
						(tenant_id, key, value, is_secret, secret_encrypted)
					VALUES ($1,'telegram.bot_token',to_jsonb(''::text),true,$2)
					ON CONFLICT (tenant_id, key)
					DO UPDATE SET secret_encrypted = EXCLUDED.secret_encrypted,
					              is_secret = true, updated_at = now()`,
					tenantID, sealed); err != nil {
					return err
				}
			}
			return nil
		})
}
