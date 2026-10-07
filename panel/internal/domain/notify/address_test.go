package notify

import (
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// 迁移种子与「恢复默认」用的 defaultTemplates 必须逐字一致，
// 否则管理员点一次恢复默认，验证码邮件就变成另一份文案。
func TestEmailVerifySeedMatchesDefaultTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/00074_auth_email_verify_template.sql")
	if err != nil {
		t.Fatal(err)
	}
	seed := string(raw)
	d, ok := defaultTemplates["auth.email_verify|email"]
	if !ok {
		t.Fatal("defaultTemplates lacks auth.email_verify|email")
	}
	if !strings.Contains(seed, "'"+d.Subject+"'") || !strings.Contains(seed, "'"+d.Body+"'") {
		t.Fatal("migration 00074 seed drifted from defaultTemplates")
	}
	for _, v := range []string{"site", "code", "minutes"} {
		if !strings.Contains(d.Subject+d.Body, "{{"+v+"}}") {
			t.Fatalf("template does not use variable %s", v)
		}
	}
	if !strings.Contains(seed, "ARRAY['site','code','minutes'], 'transactional'") {
		t.Fatal("seed must whitelist exactly site/code/minutes and be transactional")
	}
}

// Kick 在循环没跑、或者已有一次待处理时都不能阻塞调用方（注册请求）。
func TestKickNeverBlocks(t *testing.T) {
	s := New(nil, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	for i := 0; i < 3; i++ {
		s.Kick()
	}
	if len(s.kick) != 1 {
		t.Fatalf("pending kicks = %d, want coalesced to 1", len(s.kick))
	}
	var zero Service
	zero.Kick() // 字面量构造、没有 kick 通道时同样不阻塞
}

// 找回密码模板：迁移 00128 的种子与「恢复默认」逐字一致，只许 site/code/minutes。
func TestPasswordResetSeedMatchesDefaultTemplate(t *testing.T) {
	raw, err := os.ReadFile("../../../migrations/00128_auth_password_reset_template.sql")
	if err != nil {
		t.Fatal(err)
	}
	seed := string(raw)
	d, ok := defaultTemplates["auth.password_reset|email"]
	if !ok {
		t.Fatal("defaultTemplates lacks auth.password_reset|email")
	}
	if !strings.Contains(seed, "'"+d.Subject+"'") || !strings.Contains(seed, "'"+d.Body+"'") {
		t.Fatal("migration 00128 seed drifted from defaultTemplates")
	}
	for _, v := range []string{"site", "code", "minutes"} {
		if !strings.Contains(d.Subject+d.Body, "{{"+v+"}}") {
			t.Fatalf("template does not use variable %s", v)
		}
	}
	if !strings.Contains(seed, "ARRAY['site','code','minutes'], 'transactional'") {
		t.Fatal("seed must whitelist exactly site/code/minutes and be transactional")
	}
}

// 没装邮件发信器时找回密码必须关着；静态发信器只在配置齐全时才构造得出来。
func TestEmailConfiguredFollowsSender(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if New(nil, log, nil).EmailConfigured(t.Context(), "") {
		t.Fatal("no email sender must not count as configured")
	}
	if New(nil, log, nil, NewSMTPSender(SMTPConfig{})).EmailConfigured(t.Context(), "") {
		t.Fatal("an incomplete static config yields no sender and must not count as configured")
	}
	full := NewSMTPSender(SMTPConfig{Host: "smtp.example.test", Port: 465, From: "noreply@example.test"})
	if !New(nil, log, nil, full).EmailConfigured(t.Context(), "") {
		t.Fatal("a complete static config must count as configured")
	}
}
