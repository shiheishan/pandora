// [INPUT]: 依赖 platform 的 db/crypto，依赖 domain/appearance 的 SiteNameTx
// [OUTPUT]: 对外提供 DBSMTPProvider、NewDBSMTPProvider、LoadSMTPConfig、SealSMTPPassword、NewDynamicSMTPSender
// [POS]: domain/notify 的邮件配置：从 system_settings 读 SMTP 设置并短缓存，发件人名缺省取站点名
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package notify

// 数据库里的邮件设置。
//
// 从环境变量搬过来的理由很实际：换发信服务商是运营决定，
// 而改 .env 要服务器权限还要重启进程。搬到数据库之后，
// 后台改完下一封邮件就生效。
//
// 代价是每次发信都要读一次配置，所以加了一层短缓存 —— 见 cacheTTL。

import (
	"context"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/appearance"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
)

// cacheTTL 决定配置改完多久生效。
//
// 30 秒是个折中：管理员改完设置点「发送测试邮件」时不至于觉得没生效，
// 又不会让每封通知邮件都去查一次库 —— 批量发到期提醒时那是几千次查询。
const cacheTTL = 30 * time.Second

// DBSMTPProvider 从 system_settings 读取 SMTP 配置。
type DBSMTPProvider struct {
	pool     *db.Pool
	envelope *crypto.Envelope

	mu     sync.RWMutex
	cached SMTPConfig
	at     time.Time
}

func NewDBSMTPProvider(pool *db.Pool, envelope *crypto.Envelope) *DBSMTPProvider {
	return &DBSMTPProvider{pool: pool, envelope: envelope}
}

// Config 返回当前配置，必要时回源。
func (p *DBSMTPProvider) Config(ctx context.Context, tenantID string) SMTPConfig {
	p.mu.RLock()
	if time.Since(p.at) < cacheTTL {
		c := p.cached
		p.mu.RUnlock()
		return c
	}
	p.mu.RUnlock()

	cfg, err := LoadSMTPConfig(ctx, p.pool, p.envelope, tenantID)
	if err != nil {
		// 读不到就用上一次的值。数据库抖一下不该让通知全线停发，
		// 而缓存里的配置几秒钟前还是好的
		p.mu.RLock()
		defer p.mu.RUnlock()
		return p.cached
	}

	p.mu.Lock()
	p.cached, p.at = cfg, time.Now()
	p.mu.Unlock()
	return cfg
}

// Invalidate 让下一次读取回源。管理员保存设置后调用。
func (p *DBSMTPProvider) Invalidate() {
	p.mu.Lock()
	p.at = time.Time{}
	p.mu.Unlock()
}

// LoadSMTPConfig 从设置表读一份 SMTP 配置。
func LoadSMTPConfig(ctx context.Context, pool *db.Pool, envelope *crypto.Envelope,
	tenantID string) (SMTPConfig, error) {

	var cfg SMTPConfig
	var encryption string
	var pwEnc []byte

	err := pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			SELECT COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_host'), ''),
			       COALESCE((SELECT (value #>> '{}')::int FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_port'), 465),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.encryption'), 'ssl'),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_username'), ''),
			       COALESCE((SELECT secret_encrypted FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.smtp_password'), ''::bytea),
			       COALESCE((SELECT value #>> '{}' FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.from_address'), ''),
			       COALESCE((SELECT btrim(value #>> '{}') FROM system_settings
			                  WHERE tenant_id=$1 AND key='mail.from_name'), '')`,
			tenantID).Scan(&cfg.Host, &cfg.Port, &encryption, &cfg.Username,
			&pwEnc, &cfg.From, &cfg.FromName)
		if err != nil || cfg.FromName != "" {
			return err
		}
		// 没单独设发件人名时用站点名（生效主题的 branding.site_name）
		cfg.FromName, err = appearance.SiteNameTx(ctx, tx, tenantID)
		return err
	})
	if err != nil {
		return cfg, err
	}

	if len(pwEnc) > 0 && envelope != nil {
		if plain, err := envelope.Open(pwEnc, []byte("smtp")); err == nil {
			cfg.Password = string(plain)
		}
	}
	// ssl = 465 直连 TLS；tls = 587 先明文再 STARTTLS。
	// 两者名字容易混，界面上写清楚是哪个端口对应哪个
	cfg.UseTLS = encryption == "ssl"
	return cfg, nil
}

// SealSMTPPassword 加密一条 SMTP 密码，供管理端保存时使用。
func SealSMTPPassword(envelope *crypto.Envelope, plain string) ([]byte, error) {
	if plain == "" || envelope == nil {
		return nil, nil
	}
	return envelope.Seal([]byte(plain), []byte("smtp"))
}

// dynamicSender 每次发信前取一次最新配置。
type dynamicSender struct {
	provider *DBSMTPProvider
	tenantID string
}

// NewDynamicSMTPSender 返回一个配置随数据库变化的发信器。
//
// 与 NewSMTPSender 的区别是它永远返回非 nil：配置为空时在 Send 里
// 报「未配置」，而不是在启动时就把渠道整个摘掉 ——
// 否则管理员在后台填好 SMTP 之后还得重启进程才能发信。
func NewDynamicSMTPSender(provider *DBSMTPProvider, tenantID string) Sender {
	return &dynamicSender{provider: provider, tenantID: tenantID}
}

func (d *dynamicSender) Channel() Channel { return ChannelEmail }

func (d *dynamicSender) Send(ctx context.Context, to, subject, body string) error {
	cfg := d.provider.Config(ctx, d.tenantID)
	if !cfg.Enabled() {
		return ErrChannelNotConfigured
	}
	return (&SMTPSender{cfg: cfg}).Send(ctx, to, subject, body)
}
