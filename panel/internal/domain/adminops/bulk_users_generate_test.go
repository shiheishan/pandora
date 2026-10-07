package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 批量生成账号：Argon2 只在事务外算（hashGeneratedPasswords），事务里只写库。
// 原来在事务里逐个算 500 个哈希，长时间占着连接，撞 statement_timeout 与网关超时。
// 哈希经全局名额（crypto.AcquirePasswordSlot）逐个算，排队受 ctx 与排队超时约束。
func TestGenerateUsersHashesOutsideTransaction(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	gen := pkg.Decl("Service.GenerateUsers")
	hashAt := strings.Index(gen, "hashGeneratedPasswords(ctx, in.Count)")
	txAt := strings.Index(gen, "s.pool.InTx(")
	if hashAt < 0 || txAt < 0 || hashAt > txAt {
		t.Fatal("GenerateUsers must hash every password before opening the transaction")
	}
	for _, decl := range []string{gen, pkg.Decl("insertGeneratedUsers")} {
		if strings.Contains(decl, "HashPassword") || strings.Contains(decl, ".Hash(") {
			t.Fatal("no Argon2 hashing may run inside the generation transaction")
		}
	}
	hash := pkg.Decl("hashGeneratedPasswords")
	if !strings.Contains(hash, "crypto.AcquirePasswordSlot(ctx)") || !strings.Contains(hash, "slot.Hash(") {
		t.Fatal("hashGeneratedPasswords must hash through a global password slot")
	}
	if strings.Contains(hash, "crypto.HashPassword(") {
		t.Fatal("the package-level HashPassword waits without a queue timeout; use a slot")
	}
}
