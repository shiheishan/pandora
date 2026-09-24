// [INPUT]: 依赖同包 address.go 的 Kick、template_admin.go 的 defaultTemplates，读取 migrations/00074
// [OUTPUT]: 对外提供 TestEmailVerifySeedMatchesDefaultTemplate、TestKickNeverBlocks
// [POS]: domain/notify 的单元测试：验证码模板种子与默认模板一致、Kick 不阻塞
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

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
