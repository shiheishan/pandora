// [INPUT]: 依赖 service.go 的 emailVerifyEnabled 与 EmailVerificationDefault、registration_policy_test.go 的假事务，依赖迁移 00030 / 00042 的种子与 platform/sourcetest（读 api/admin 的 getMailSettings）
// [OUTPUT]: 对外提供 TestEmailVerificationDefaultMatchesSeed
// [POS]: identity 的单元测试：auth.email_verification 缺行时注册流程与后台邮件页用同一个回退值，且等于迁移种子（⑩）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package identity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestEmailVerificationDefaultMatchesSeed(t *testing.T) {
	// 迁移种子：00030 给当时的租户种行，00042 给之后补种，两处都是关
	seeds := map[string]string{
		"00030_mail_settings.sql":          `('auth.email_verification', 'false'::jsonb`,
		"00042_seed_registration_mode.sql": `'auth.email_verification', to_jsonb(false)`,
	}
	for file, literal := range seeds {
		body, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), literal) {
			t.Fatalf("%s no longer seeds %s; update EmailVerificationDefault together with the seed", file, literal)
		}
	}
	if EmailVerificationDefault != false {
		t.Fatal("EmailVerificationDefault must equal the migration seed (false)")
	}

	// 注册流程：缺行按回退值
	on, err := emailVerifyEnabled(context.Background(),
		&registrationPolicyTx{emailErr: pgx.ErrNoRows}, "tenant")
	if err != nil || on != EmailVerificationDefault {
		t.Fatalf("missing setting: email verification=%v err=%v, want %v", on, err, EmailVerificationDefault)
	}

	// 后台邮件页：同一个常量，不再写字面量
	mail := sourcetest.Load(t, filepath.Join("..", "..", "api", "admin")).Decl("handlers.getMailSettings")
	if !strings.Contains(mail, "identity.EmailVerificationDefault") ||
		strings.Contains(mail, "key='auth.email_verification'), false)") ||
		strings.Contains(mail, "key='auth.email_verification'), true)") {
		t.Fatal("admin mail settings must fall back to identity.EmailVerificationDefault")
	}
}
