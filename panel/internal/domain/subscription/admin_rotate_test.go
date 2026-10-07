package subscription

import (
	"reflect"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// D-B-1：管理员换发链接的结果里不能有任何能携带令牌明文的字段，
// 这样无论处理器怎么拼响应，都拿不到新令牌。
func TestAdminRotateOutputHoldsNoToken(t *testing.T) {
	typ := reflect.TypeOf(AdminRotateOutput{})
	if typ.NumField() != 1 || typ.Field(0).Name != "UserEmail" {
		t.Fatalf("AdminRotateOutput fields changed: %v; a new field must not carry the subscription token", typ)
	}
}

// 后台换发订阅链接：换发与审计在同一个事务里（审计台账 2.3 第 4 条），审计写不进去换发也回滚。
func TestAdminRotateAuditsInsideRotationTransaction(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.AdminRotate")
	if n := strings.Count(body, "s.pool.InTx("); n != 1 {
		t.Fatalf("AdminRotate opens %d transactions, want exactly one", n)
	}
	tx := strings.Index(body, "s.pool.InTx(")
	rotate := strings.Index(body, "s.rotateInTx(ctx, tx,")
	auditAt := strings.Index(body, "audit.Write(ctx, tx,")
	if tx < 0 || rotate < tx || auditAt < rotate {
		t.Fatal("AdminRotate must rotate and audit in the same transaction, audit last")
	}
	if strings.Contains(body, "s.Rotate(") {
		t.Fatal("AdminRotate must not commit the rotation in a separate transaction")
	}
}
