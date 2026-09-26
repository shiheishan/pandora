// [INPUT]: 依赖 platform/sourcetest 按名取 handlers.assignNodePool 与 handlers.deleteNodePool 的源码
// [OUTPUT]: 对外提供 TestDirectPoolAssignmentIsFrozenUntilEffectiveReleases、TestPoolDeletionLocksParentBeforeDependencyCounts
// [POS]: api/admin 节点池的并发契约：直接改分组被冻结到有效发布就绪、删池先锁父行再数依赖
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestDirectPoolAssignmentIsFrozenUntilEffectiveReleases(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("handlers.assignNodePool")
	for _, needle := range []string{
		`SELECT pool_id::text FROM nodes`,
		`FOR UPDATE`,
		`if currentID != req.PoolID`,
		`httpx.CodeConflict`,
		`配置发布身份升级完成前暂不允许移动节点分组`,
		`same-pool idempotent replay`,
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("pool assignment freeze contract missing %q", needle)
		}
	}
}

func TestPoolDeletionLocksParentBeforeDependencyCounts(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("handlers.deleteNodePool")
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
