package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestDirectPoolAssignmentIsFrozenUntilEffectiveReleases(t *testing.T) {
	nodes := sourcetest.Load(t, "../../domain/nodefabric")
	check := nodes.Decl("Service.CheckNodePoolAssignment")
	for _, needle := range []string{
		`SELECT pool_id::text FROM nodes`,
		`FOR UPDATE`,
		`if currentID != poolID`,
		`return ErrNodePoolMoveFrozen`,
		`same-pool idempotent replay`,
	} {
		if !strings.Contains(check, needle) {
			t.Fatalf("pool assignment freeze contract missing %q", needle)
		}
	}
	src := sourcetest.Load(t, ".").Decl("handlers.assignNodePool")
	for _, needle := range []string{
		`h.d.Node.CheckNodePoolAssignment(`,
		`errors.Is(err, nodefabric.ErrNodePoolMoveFrozen)`,
		`httpx.CodeConflict`,
		`配置发布身份升级完成前暂不允许移动节点分组`,
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("pool assignment freeze contract missing %q", needle)
		}
	}
}

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
