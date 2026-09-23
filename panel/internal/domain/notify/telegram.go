package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// Telegram 通知（对标 Xboard 的 config/setTelegramWebhook + user/telegram）。
//
// 绑定必须双向验证：面板发一个一次性验证码，用户在 Telegram 里发给 bot，
// bot 的 webhook 带着 chat_id 回来核对。只让用户在面板里填 chat_id 是不行的 ——
// 填别人的 chat_id 就能把别人账号的通知劫持到自己那里。

const ChannelTelegram Channel = "telegram"

// BindCodeTTL 给用户从面板切到 Telegram 再粘贴的时间。
// 太短会让人来不及，太长会让一串能绑定账号的码在剪贴板里躺很久。
const BindCodeTTL = 10 * time.Minute

//-----------------------------------------------------------------------------
// Bot 配置
//-----------------------------------------------------------------------------

type TelegramConfig struct {
	Enabled       bool
	BotToken      string
	BotUsername   string
	WebhookSecret string
}

func (c TelegramConfig) Ready() bool {
	return c.Enabled && c.BotToken != "" && c.BotUsername != ""
}

func LoadTelegramConfig(ctx context.Context, pool *db.Pool, env *crypto.Envelope,
	tenantID string) (TelegramConfig, error) {

	var cfg TelegramConfig
	var sealed, webhookSealed []byte
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT (value #>> '{}')::boolean FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.enabled'), false),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.bot_username'), ''),
			       COALESCE((SELECT secret_encrypted FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.bot_token'), ''::bytea),
			       COALESCE((SELECT secret_encrypted FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.webhook_secret'), ''::bytea)`,
			tenantID).Scan(&cfg.Enabled, &cfg.BotUsername, &sealed, &webhookSealed)
	})
	if err != nil {
		return cfg, err
	}
	// aad 用 "telegram"：和 SMTP 口令用不同的关联数据，这样即使有人
	// 把一条密文从 mail.smtp_password 搬到 telegram.bot_token，也解不开。
	if len(sealed) != 0 {
		plain, err := env.Open(sealed, []byte("telegram"))
		if err != nil {
			return cfg, fmt.Errorf("解密 Bot Token: %w", err)
		}
		cfg.BotToken = string(plain)
	}
	if len(webhookSealed) != 0 {
		plain, err := env.Open(webhookSealed, []byte("telegram_webhook"))
		if err != nil {
			return cfg, fmt.Errorf("解密 Telegram webhook secret: %w", err)
		}
		cfg.WebhookSecret = string(plain)
	}
	return cfg, nil
}

// EnsureTelegramWebhookSecret creates the independent high-entropy credential
// used in the webhook URL and keeps it encrypted at rest.
func EnsureTelegramWebhookSecret(ctx context.Context, pool *db.Pool, env *crypto.Envelope,
	tenantID string) error {
	return pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM system_settings
			 WHERE tenant_id=$1 AND key='telegram.webhook_secret'
			   AND length(secret_encrypted) > 0)`, tenantID).Scan(&exists); err != nil {
			return err
		}
		if exists {
			return nil
		}
		secret, err := crypto.NewToken(32)
		if err != nil {
			return err
		}
		sealed, err := env.Seal([]byte(secret), []byte("telegram_webhook"))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO system_settings
				(tenant_id, key, value, is_secret, secret_encrypted)
			VALUES ($1, 'telegram.webhook_secret', to_jsonb(''::text), true, $2)
			ON CONFLICT (tenant_id, key) DO NOTHING`, tenantID, sealed)
		return err
	})
}

//-----------------------------------------------------------------------------
// 发送器
//-----------------------------------------------------------------------------

// TelegramSender 通过 Bot API 发消息。
//
// 与 SMTP 发送器并列注册进 notify.Service，所以到期提醒、流量预警这些
// 已有的通知不用改一行代码就能多一个投递渠道 —— 它们只认 template_code，
// 渠道由模板表决定。
type TelegramSender struct {
	token  string
	client *http.Client
}

func NewTelegramSender(token string) *TelegramSender {
	if strings.TrimSpace(token) == "" {
		return nil
	}
	return &TelegramSender{
		token: token,
		// 超时必须有：Telegram 的 API 偶尔会挂住，而派发是串行的，
		// 一条卡住会把整批通知堵死。
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

func (t *TelegramSender) Channel() Channel { return ChannelTelegram }

// Send 的 recipient 是 chat_id 的十进制字符串。
func (t *TelegramSender) Send(ctx context.Context, recipient, subject, body string) error {
	chatID, err := strconv.ParseInt(strings.TrimSpace(recipient), 10, 64)
	if err != nil {
		return fmt.Errorf("telegram: 非法的 chat_id %q", recipient)
	}

	text := body
	if subject != "" {
		text = "*" + escapeMarkdown(subject) + "*\n\n" + escapeMarkdown(body)
	}
	payload, err := json.Marshal(map[string]any{
		"chat_id": chatID, "text": text, "parse_mode": "MarkdownV2",
		"disable_web_page_preview": true,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.telegram.org/bot"+t.token+"/sendMessage", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 限制读取长度：出错时 Telegram 会回一段 JSON，正常时也不长。
	// 不限的话，一个异常响应就能把内存吃掉。
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
	if resp.StatusCode/100 != 2 {
		// 把 Telegram 的原文带出来：管理员要靠它区分「token 错了」
		// 和「用户把 bot 拉黑了」，包装成「发送失败」等于什么都没说。
		return fmt.Errorf("telegram: HTTP %d %s", resp.StatusCode,
			strings.TrimSpace(string(raw)))
	}
	return nil
}

// escapeMarkdown 转义 MarkdownV2 的保留字符。
//
// 不转义的话，用户名里一个下划线就会让整条消息发送失败（400），
// 而那种失败在日志里看起来像是随机的。
func escapeMarkdown(s string) string {
	const special = "_*[]()~`>#+-=|{}.!"
	var b strings.Builder
	b.Grow(len(s) + 16)
	for _, r := range s {
		if strings.ContainsRune(special, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// DynamicTelegramSender 每次发送前按需读配置，避免改了 token 要重启。
//
// 与 DBSMTPProvider 同样的思路和同样的 30 秒缓存：Bot Token 改动不频繁，
// 但每条消息都去查一次库在批量派发时是实打实的开销。
type DynamicTelegramSender struct {
	pool     *db.Pool
	env      *crypto.Envelope
	tenantID string

	mu       sync.Mutex
	cached   *TelegramSender
	cachedAt time.Time
}

func NewDynamicTelegramSender(pool *db.Pool, env *crypto.Envelope, tenantID string) *DynamicTelegramSender {
	return &DynamicTelegramSender{pool: pool, env: env, tenantID: tenantID}
}

func (d *DynamicTelegramSender) Channel() Channel { return ChannelTelegram }

func (d *DynamicTelegramSender) Send(ctx context.Context, recipient, subject, body string) error {
	d.mu.Lock()
	if d.cached != nil && time.Since(d.cachedAt) < 30*time.Second {
		s := d.cached
		d.mu.Unlock()
		return s.Send(ctx, recipient, subject, body)
	}
	d.mu.Unlock()

	token, enabled, err := loadTelegramToken(ctx, d.pool, d.env, d.tenantID)
	if err != nil {
		return err
	}
	if !enabled || token == "" {
		return errors.New("telegram: 未启用或未配置 Bot Token")
	}
	s := NewTelegramSender(token)
	if s == nil {
		return errors.New("telegram: Bot Token 无效")
	}
	d.mu.Lock()
	d.cached, d.cachedAt = s, time.Now()
	d.mu.Unlock()
	return s.Send(ctx, recipient, subject, body)
}

// Invalidate 让缓存立刻失效，供管理员保存配置后调用。
func (d *DynamicTelegramSender) Invalidate() {
	d.mu.Lock()
	d.cached = nil
	d.mu.Unlock()
}

func loadTelegramToken(ctx context.Context, pool *db.Pool, env *crypto.Envelope,
	tenantID string) (string, bool, error) {

	var sealed []byte
	var enabled bool
	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT (value #>> '{}')::boolean FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.enabled'), false),
			       COALESCE((SELECT secret_encrypted FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.bot_token'), ''::bytea)`,
			tenantID).Scan(&enabled, &sealed)
	})
	if err != nil || len(sealed) == 0 {
		return "", enabled, err
	}
	plain, err := env.Open(sealed, []byte("telegram"))
	if err != nil {
		return "", enabled, fmt.Errorf("解密 Bot Token: %w", err)
	}
	return string(plain), enabled, nil
}

//-----------------------------------------------------------------------------
// 绑定
//-----------------------------------------------------------------------------

type BindCode struct {
	Code        string    `json:"code"`
	BotUsername string    `json:"bot_username"`
	ExpiresAt   time.Time `json:"expires_at"`
}

// IssueBindCode 给用户发一个一次性绑定码。
func (s *Service) IssueBindCode(ctx context.Context, tenantID, userID string) (*BindCode, error) {
	var botUser string
	var enabled bool
	if err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT (value #>> '{}')::boolean FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.enabled'), false),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.bot_username'), '')`,
			tenantID).Scan(&enabled, &botUser)
	}); err != nil {
		return nil, err
	}
	if !enabled || botUser == "" {
		return nil, httpx.New(httpx.CodeValidationFailed,
			"站点还没有启用 Telegram 通知")
	}

	code, err := newBindCode()
	if err != nil {
		return nil, err
	}
	expires := time.Now().Add(BindCodeTTL)

	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		var bound bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM telegram_bindings
			               WHERE tenant_id=$1 AND user_id=$2::uuid)`,
			tenantID, userID).Scan(&bound); err != nil {
			return err
		}
		if bound {
			return httpx.New(httpx.CodeConflict,
				"你已经绑定过 Telegram 了，要换一个请先解绑")
		}
		// 同一个用户只留最新一个码：连点两次「获取验证码」时，
		// 前一个应当立刻作废，否则外面会有两个都能用的码。
		if _, err := tx.Exec(ctx, `
			DELETE FROM telegram_bind_codes
			 WHERE tenant_id=$1 AND user_id=$2::uuid AND used_at IS NULL`,
			tenantID, userID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO telegram_bind_codes (tenant_id, user_id, code, expires_at)
			VALUES ($1,$2::uuid,$3,$4)`, tenantID, userID, code, expires)
		return err
	})
	if err != nil {
		return nil, err
	}
	return &BindCode{Code: code, BotUsername: botUser, ExpiresAt: expires}, nil
}

// ConfirmBind 由 webhook 调用：用户在 Telegram 里发来了验证码。
func (s *Service) ConfirmBind(ctx context.Context, tenantID, code string,
	chatID int64, username string) (string, error) {

	code = strings.ToUpper(strings.TrimSpace(code))
	if len(code) < 6 || len(code) > 12 {
		return "", errors.New("bind code out of range")
	}

	var userID string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		var codeID string
		err := tx.QueryRow(ctx, `
			SELECT id::text, user_id::text FROM telegram_bind_codes
			 WHERE tenant_id=$1 AND code=$2 AND used_at IS NULL AND expires_at > now()
			 FOR UPDATE`, tenantID, code).Scan(&codeID, &userID)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("验证码无效或已过期")
		}
		if err != nil {
			return err
		}

		tag, err := tx.Exec(ctx, `
			UPDATE telegram_bind_codes SET used_at = now()
			 WHERE tenant_id=$1 AND id=$2::uuid AND used_at IS NULL`, tenantID, codeID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("验证码已被使用")
		}

		if _, err := tx.Exec(ctx, `
			INSERT INTO telegram_bindings (tenant_id, user_id, chat_id, username)
			VALUES ($1,$2::uuid,$3,$4)`, tenantID, userID, chatID, username); err != nil {
			if db.IsUniqueViolation(err) {
				return errors.New("这个 Telegram 账号已经绑定了其它用户")
			}
			return err
		}

		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "telegram.bound", ResourceType: "user", ResourceID: &userID,
			AfterDigest: map[string]any{"username": username},
			APIDomain:   "public", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
	return userID, err
}

func (s *Service) Unbind(ctx context.Context, tenantID, userID string) error {
	return s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM telegram_bindings WHERE tenant_id=$1 AND user_id=$2::uuid`,
			tenantID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return httpx.New(httpx.CodeNotFound, "你还没有绑定 Telegram")
		}
		return audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "user", ActorID: &userID,
			Action: "telegram.unbound", ResourceType: "user", ResourceID: &userID,
			APIDomain: "public", RequestID: httpx.RequestIDFrom(ctx),
		})
	})
}

type BindingInfo struct {
	Bound       bool   `json:"bound"`
	Username    string `json:"username,omitempty"`
	BotUsername string `json:"bot_username,omitempty"`
	Enabled     bool   `json:"enabled"`
}

func (s *Service) BindingInfo(ctx context.Context, tenantID, userID string) (*BindingInfo, error) {
	out := &BindingInfo{}
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: userID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT (value #>> '{}')::boolean FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.enabled'), false),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='telegram.bot_username'), ''),
			       EXISTS(SELECT 1 FROM telegram_bindings
			               WHERE tenant_id=$1 AND user_id=$2::uuid),
			       COALESCE((SELECT username FROM telegram_bindings
			                  WHERE tenant_id=$1 AND user_id=$2::uuid), '')`,
			tenantID, userID).Scan(&out.Enabled, &out.BotUsername, &out.Bound, &out.Username)
	})
	return out, err
}

// newBindCode 生成绑定码。
//
// 字母表同样避开了 I/L/O/0/1 —— 这串码要用户在两个应用之间手工传递，
// 看错一个字符就是一次失败的绑定和一个「为什么绑不上」的工单。
func newBindCode() (string, error) {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	out := make([]byte, 8)
	for i, v := range b {
		out[i] = alphabet[int(v)%len(alphabet)]
	}
	return string(out), nil
}
