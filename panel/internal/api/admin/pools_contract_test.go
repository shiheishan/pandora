// [INPUT]: 依赖 validateSetPlanPoolsRequest、domain/adminops 的 ValidateEditablePlanPoolVersion、platform/httpx 的错误码，依赖 platform/sourcetest 按名取套餐绑池处理器与 adminops.PlanPools / SetPlanPools、nodefabric.UpdateNodePool 的源码
// [OUTPUT]: 对外提供 TestSetPlanPoolsRequestValidation、TestPlanPoolDraftAndConflictContract、TestPlanPoolHandlerSourceContract、TestPoolUpdatePreservesLifecycleLockContract
// [POS]: api/admin 套餐绑池的校验、草稿与冲突语义、审计口径（SQL 与审计已下沉 adminops，这里跨包读源码），节点池更新保持生命周期锁（SQL 在 nodefabric）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"errors"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

const (
	poolContractPlanID    = "00000000-0000-7000-8000-000000000101"
	poolContractVersionID = "00000000-0000-7000-8000-000000000102"
	poolContractPoolID    = "00000000-0000-7000-8000-000000000103"
)

func catalogPoolError(t *testing.T, err error) *httpx.Error {
	t.Helper()
	var out *httpx.Error
	if !errors.As(err, &out) {
		t.Fatalf("error type = %T, want *httpx.Error", err)
	}
	return out
}

func TestSetPlanPoolsRequestValidation(t *testing.T) {
	valid := setPlanPoolsReq{
		VersionID: poolContractVersionID, ExpectedVersionRowVersion: 3,
		PoolIDs: []string{poolContractPoolID},
	}
	got, err := validateSetPlanPoolsRequest(poolContractPlanID, valid)
	if err != nil || len(got) != 1 || got[0] != poolContractPoolID {
		t.Fatalf("valid request pools=%v err=%v", got, err)
	}

	bad := valid
	bad.ExpectedVersionRowVersion = 0
	if he := catalogPoolError(t, mustSetPlanPoolsValidationError(poolContractPlanID, bad)); he.Code != httpx.CodeValidationFailed {
		t.Fatalf("missing optimistic token code=%s, want validation_failed", he.Code)
	}

	bad = valid
	bad.PoolIDs = []string{poolContractPoolID, poolContractPoolID}
	if he := catalogPoolError(t, mustSetPlanPoolsValidationError(poolContractPlanID, bad)); he.Code != httpx.CodeValidationFailed {
		t.Fatalf("duplicate pool code=%s, want validation_failed", he.Code)
	}

	if he := catalogPoolError(t, mustSetPlanPoolsValidationError("not-a-uuid", valid)); he.Code != httpx.CodeNotFound {
		t.Fatalf("malformed plan code=%s, want not_found", he.Code)
	}
}

func mustSetPlanPoolsValidationError(planID string, req setPlanPoolsReq) error {
	_, err := validateSetPlanPoolsRequest(planID, req)
	return err
}

func TestPlanPoolDraftAndConflictContract(t *testing.T) {
	if err := adminops.ValidateEditablePlanPoolVersion("draft", false, 4, 4); err != nil {
		t.Fatalf("editable draft rejected: %v", err)
	}
	for _, tc := range []struct {
		name    string
		status  string
		frozen  bool
		current int64
		expect  int64
	}{
		{name: "published", status: "published", current: 4, expect: 4},
		{name: "frozen", status: "draft", frozen: true, current: 4, expect: 4},
		{name: "stale", status: "draft", current: 5, expect: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			he := catalogPoolError(t, adminops.ValidateEditablePlanPoolVersion(tc.status, tc.frozen, tc.current, tc.expect))
			if he.Code != httpx.CodeConflict {
				t.Fatalf("code=%s, want conflict", he.Code)
			}
			if tc.name == "stale" && he.Fields["row_version"] != "current=5" {
				t.Fatalf("stale conflict fields=%v", he.Fields)
			}
		})
	}
}

func TestPlanPoolHandlerSourceContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	ops := sourcetest.Load(t, "../../domain/adminops")
	src := ops.Decls("Service.PlanPools", "Service.SetPlanPools")
	for _, required := range []string{
		"status='draft' AND frozen_at IS NULL",
		"FOR UPDATE OF pv",
		"FOR KEY SHARE",
		"row_version=row_version+1",
		`Action: "plan_version.pools_changed"`,
		`ResourceType: "plan_version"`,
		`db.Scope{TenantID: tenantID, ActorID: actorID}`,
		`BeforeDigest: map[string]any{"row_version": current, "pool_ids": before}`,
		`AfterDigest:  map[string]any{"row_version": next, "pool_ids": poolIDs, "plan_id": planID}`,
	} {
		if !strings.Contains(src, required) {
			t.Fatalf("plan pool contract missing %q", required)
		}
	}
	handler := pkg.Decl("handlers.planPools")
	for _, required := range []string{"out.VersionStatus", "out.RowVersion", "out.Editable"} {
		if !strings.Contains(handler, required) {
			t.Fatalf("plan pool response missing %q", required)
		}
	}
	for _, p := range []*sourcetest.Package{pkg, ops} {
		if strings.Contains(p.Source(), `Action: "plan.pools_changed"`) {
			t.Fatal("legacy published-plan pool mutation audit action returned")
		}
	}
}

func TestPoolUpdatePreservesLifecycleLockContract(t *testing.T) {
	block := sourcetest.Load(t, "../../domain/nodefabric").Decl("Service.UpdateNodePool")
	updateAt := strings.Index(block, `UPDATE node_pools`)
	auditAt := strings.Index(block, `"node_pool.updated"`)
	if updateAt < 0 || auditAt <= updateAt {
		t.Fatalf("pool lifecycle update/audit order drifted: update=%d audit=%d", updateAt, auditAt)
	}
	for _, required := range []string{
		`status = COALESCE(NULLIF($5,''), status)`,
		`WHERE tenant_id = $1 AND id = $2::uuid`,
		`if tag.RowsAffected() == 0`,
		`httpx.NotFoundOrForbidden()`,
		`map[string]any{"name": in.Name, "status": in.Status}`,
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("pool lifecycle update contract missing %q", required)
		}
	}
}
