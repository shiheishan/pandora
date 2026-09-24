// [INPUT]: 依赖 logout_pg18_test.go 的 openLogoutPG18Fixture，依赖 service.go 的 StartRegistration，依赖 domain/notify 的 Service（真实入队与派发）
// [OUTPUT]: 对外提供 TestRegistrationVerificationMailPG18
// [POS]: domain/identity 的 PG18 集成测试：注册验证码从入队、派发到清空 payload 的全链路（缺陷 1 / 迁移 00074）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/notify"
	"github.com/aegispanel/aegis/internal/platform/crypto"
)

// captureSender 记下每一封「发出去」的邮件，不出网。
type captureSender struct {
	mu   sync.Mutex
	sent []capturedMail
}

type capturedMail struct{ to, subject, body string }

func (c *captureSender) Channel() notify.Channel { return notify.ChannelEmail }

func (c *captureSender) Send(_ context.Context, to, subject, body string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, capturedMail{to, subject, body})
	return nil
}

func TestRegistrationVerificationMailPG18(t *testing.T) {
	ctx, admin, app := openLogoutPG18Fixture(t)

	// 迁移 00074 对迁移时已存在的租户（00010 种下的默认租户）补了模板
	const defaultTenant = "00000000-0000-7000-8000-000000000001"
	var category, status string
	var vars []string
	if err := admin.QueryRow(ctx, `
		SELECT category, status, allowed_variables FROM notification_templates
		 WHERE tenant_id=$1 AND code='auth.email_verify' AND channel='email' AND locale='zh-CN'`,
		defaultTenant).Scan(&category, &status, &vars); err != nil {
		t.Fatalf("migration 00074 did not seed the default tenant: %v", err)
	}
	if category != "transactional" || status != "active" || strings.Join(vars, ",") != "site,code,minutes" {
		t.Fatalf("seeded template shape category=%s status=%s vars=%v", category, status, vars)
	}

	const (
		tenantID      = "73000000-0000-7000-8000-000000000001"
		existingUser  = "73000000-0000-7000-8000-000000000011"
		existingEmail = "taken@registration-mail.example.test"
		newEmail      = "fresh@registration-mail.example.test"
	)
	for _, seed := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO tenants (id, slug, display_name, default_currency) VALUES ($1, 'registration-mail-pg18', 'Registration Mail PG18', 'USD')`, []any{tenantID}},
		{`INSERT INTO users (id, tenant_id, email, display_name, status) VALUES ($2, $1, $3, 'Taken', 'active')`, []any{tenantID, existingUser, existingEmail}},
		{`INSERT INTO feature_switches (tenant_id, code, enabled) VALUES ($1, 'auth.registration', true)`, []any{tenantID}},
		{`INSERT INTO system_settings (tenant_id, key, value) VALUES
		    ($1, 'auth.registration_mode', to_jsonb('open'::text)),
		    ($1, 'auth.email_verification', to_jsonb(true))`, []any{tenantID}},
		// 新租户没有赶上迁移，照默认租户的种子抄一份
		{`INSERT INTO notification_templates
		    (tenant_id, code, channel, locale, version, subject, body, allowed_variables, category, status)
		  SELECT $1, code, channel, locale, version, subject, body, allowed_variables, category, status
		    FROM notification_templates
		   WHERE tenant_id=$2 AND code='auth.email_verify' AND channel='email'`, []any{tenantID, defaultTenant}},
	} {
		if _, err := admin.Exec(ctx, seed.sql, seed.args...); err != nil {
			t.Fatalf("seed registration mail fixture: %v", err)
		}
	}

	sender := &captureSender{}
	mailer := notify.New(app, slog.New(slog.NewTextHandler(io.Discard, nil)), []byte("registration-mail-pg18-salt"), sender)
	svc := &Service{pool: app, hashSalt: []byte("registration-mail-pg18-hash")}
	svc.SetVerificationMailer(mailer)

	out, err := svc.StartRegistration(ctx, tenantID, StartRegistrationInput{Email: newEmail, UserAgent: "pg18"})
	if err != nil {
		t.Fatalf("start registration: %v", err)
	}
	if !out.VerificationRequired || out.DevCode != "" {
		t.Fatalf("production start must require verification without dev code: %+v", out)
	}

	// 入队：没有 user_id，收件地址与验证码随行，与验证码同事务落库
	var queued int
	var payload map[string]any
	if err := admin.QueryRow(ctx, `
		SELECT count(*), (array_agg(payload))[1] FROM notification_deliveries
		 WHERE tenant_id=$1 AND template_code='auth.email_verify' AND channel='email'
		   AND user_id IS NULL AND status='queued'`, tenantID).Scan(&queued, &payload); err != nil {
		t.Fatal(err)
	}
	if queued != 1 || payload["_recipient"] != newEmail || payload["minutes"] != "10" {
		t.Fatalf("queued verification delivery count=%d payload=%v", queued, payload)
	}

	if _, err := mailer.Dispatch(ctx, tenantID, 10); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(sender.sent) != 1 {
		t.Fatalf("sent %d mails, want 1", len(sender.sent))
	}
	mail := sender.sent[0]
	code := regexp.MustCompile(`\b\d{6}\b`).FindString(mail.body)
	if mail.to != newEmail || code == "" || !strings.Contains(mail.subject, code) ||
		!strings.Contains(mail.body, "10 分钟") || strings.Contains(mail.body, "{{") ||
		strings.Contains(mail.body, "_recipient") {
		t.Fatalf("rendered verification mail to=%q subject=%q body=%q", mail.to, mail.subject, mail.body)
	}
	// 发出去的正是库里那枚验证码
	var codeHash []byte
	if err := admin.QueryRow(ctx, `
		SELECT code_hash FROM verification_codes
		 WHERE tenant_id=$1 AND purpose='email_verify' AND target_hash=$2`,
		tenantID, crypto.HashIdentifier(svc.hashSalt, newEmail)).Scan(&codeHash); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(codeHash, crypto.HashToken(code)) {
		t.Fatal("mailed code does not match the stored verification code")
	}
	// 终态后 payload 清空：库里不再留收件邮箱与验证码明文
	var sentStatus string
	var scrubbed map[string]any
	if err := admin.QueryRow(ctx, `
		SELECT status, payload FROM notification_deliveries
		 WHERE tenant_id=$1 AND template_code='auth.email_verify'`, tenantID).Scan(&sentStatus, &scrubbed); err != nil {
		t.Fatal(err)
	}
	if sentStatus != "sent" || len(scrubbed) != 0 {
		t.Fatalf("delivered row status=%s payload=%v, want sent with empty payload", sentStatus, scrubbed)
	}

	// 反向：已注册的邮箱照常得到同样的响应，但不入队（IAM-006：不能借此探测邮箱）
	taken, err := svc.StartRegistration(ctx, tenantID, StartRegistrationInput{Email: existingEmail, UserAgent: "pg18"})
	if err != nil || !taken.VerificationRequired || taken.RegistrationToken == "" {
		t.Fatalf("existing email start must look identical: out=%+v err=%v", taken, err)
	}
	var total int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM notification_deliveries WHERE tenant_id=$1`, tenantID).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("existing email enqueued a mail: deliveries=%d", total)
	}
}
