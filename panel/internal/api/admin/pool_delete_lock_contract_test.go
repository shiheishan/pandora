package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 换池冻结接口（assignNodePool / CheckNodePoolAssignment）已删（用户定：允许在后台换池，
// 走 PATCH /nodes/{id} 的 pool_id，锁序见 nodefabric 的 TestPatchAdminNodePoolMoveMaterializesUnderReleaseLock）。
// 这里只钉删池的锁序。
func TestPoolDeletionLocksParentBeforeDependencyCounts(t *testing.T) {
	src := sourcetest.Load(t, "../../domain/nodefabric").Decl("Service.DeleteNodePool")
	advisoryAt := strings.Index(src, `"node-config-release/"+tenantID`)
	lockAt := strings.Index(src, `SELECT id::text FROM node_pools`)
	countAt := strings.Index(src, `SELECT (SELECT count(*) FROM nodes`)
	deleteAt := strings.Index(src, `DELETE FROM node_pools`)
	if advisoryAt < 0 || lockAt <= advisoryAt || countAt <= lockAt || deleteAt <= countAt ||
		!strings.Contains(src[lockAt:countAt], `FOR UPDATE`) {
		t.Fatalf("pool delete lock order drifted: advisory=%d lock=%d count=%d delete=%d",
			advisoryAt, lockAt, countAt, deleteAt)
	}
	for _, needle := range []string{
		`FROM node_templates WHERE tenant_id=$1 AND default_pool_id=$2::uuid`,
		`FROM node_configs`,
		`scope='pool' AND scope_ref=$2::uuid`,
		`FROM bootstrap_tokens`,
		`consumed_at IS NULL AND expires_at>now() AND used_count<max_uses`,
		`if templates > 0`,
		`if configs > 0`,
		`if activeBootstrapTokens > 0`,
	} {
		if !strings.Contains(src[lockAt:deleteAt], needle) {
			t.Fatalf("pool delete placement dependency contract missing %q", needle)
		}
	}
}
