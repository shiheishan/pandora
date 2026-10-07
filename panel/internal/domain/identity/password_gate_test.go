package identity

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 本包的口令计算一律经名额（crypto.PasswordSlot）：包级入口会自己再去排同一道闸，
// 持着名额的 goroutine 调它就是自己等自己；也会让请求路径失去排队超时与 ctx。
func TestIdentityHashesOnlyThroughPasswordSlots(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	if refs := pkg.Refs("github.com/aegispanel/aegis/internal/platform/crypto",
		"HashPassword", "VerifyPassword", "DummyVerify"); len(refs) != 0 {
		t.Fatalf("identity must hash through crypto.AcquirePasswordSlot, found package-level calls: %+v", refs)
	}
}

// 每个要算口令的入口都在开事务之前取名额：排队的请求不占数据库连接。
func TestPasswordSlotIsAcquiredBeforeTransactions(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	for _, name := range []string{
		"Service.Login", "Service.CompleteRegistration", "Service.ChangePassword",
		"Service.Reauth", "Service.AdminResetPassword",
	} {
		body := pkg.Decl(name)
		acquire := strings.Index(body, "acquirePasswordSlot(ctx)")
		release := strings.Index(body, "defer slot.Release()")
		if acquire < 0 || release < acquire {
			t.Errorf("%s must acquire a password slot and defer its release", name)
			continue
		}
		// Login 先用一个只读事务取账号，再在两个事务之间计算；其余入口在唯一的事务之前取
		firstTx := strings.Index(body, "s.pool.InTx(")
		if name == "Service.Login" {
			if lastTx := strings.LastIndex(body, "s.pool.InTx("); !(firstTx < acquire && acquire < lastTx) {
				t.Errorf("Login must compute between its lookup and session transactions")
			}
			continue
		}
		if firstTx < acquire {
			t.Errorf("%s opens a transaction before acquiring its password slot", name)
		}
	}
}
