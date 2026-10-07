package nodefabric

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 防重放只靠主键冲突；请求路径上不再删行（清理挪到后台，争同一批行的锁等待就没了）。
func TestClaimSignedRequestNoLongerDeletesInline(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	claim := pkg.Decl("Service.claimNonceInDatabase")
	if strings.Contains(claim, "DELETE") {
		t.Fatal("ClaimSignedRequest must not delete expired nonces on the request path")
	}
	for _, needle := range []string{"ON CONFLICT (tenant_id, node_id, nonce) DO NOTHING", "RETURNING `+deliveryEpochSQL"} {
		if !strings.Contains(claim, needle) {
			t.Fatalf("replay guard lost %q", needle)
		}
	}
	purge := pkg.Decl("Service.PurgeExpiredNonces")
	if !strings.Contains(purge, "expires_at < now()") || strings.Contains(purge, "FOR UPDATE") {
		t.Fatal("nonce purge must only delete expired rows and cannot lock (aegis_app has no UPDATE)")
	}
}
