package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 批量生成账号：Argon2 只在事务外算（hashGeneratedPasswords），事务里只写库。
// 原来在事务里逐个算 500 个哈希，长时间占着连接，撞 statement_timeout 与网关超时。
func TestGenerateUsersHashesOutsideTransaction(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	gen := pkg.Decl("Service.GenerateUsers")
	hashAt := strings.Index(gen, "hashGeneratedPasswords(ctx, in.Count)")
	txAt := strings.Index(gen, "s.pool.InTx(")
	if hashAt < 0 || txAt < 0 || hashAt > txAt {
		t.Fatal("GenerateUsers must hash every password before opening the transaction")
	}
	if strings.Contains(gen, "HashPassword") || strings.Contains(pkg.Decl("insertGeneratedUsers"), "HashPassword") {
		t.Fatal("no Argon2 hashing may run inside the generation transaction")
	}
	if !strings.Contains(pkg.Decl("hashGeneratedPasswords"), "crypto.HashPassword(") {
		t.Fatal("hashGeneratedPasswords is the one place generated passwords are hashed")
	}
}
