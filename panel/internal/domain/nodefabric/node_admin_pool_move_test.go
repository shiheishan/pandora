package nodefabric

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// PATCH 换池会改变节点适用的 pool 层配置：必须先拿配置发布锁、再锁节点行（与发布、
// 新建、退役同一锁序），UPDATE 之后在锁内按新池重新物化 desired 版本。少了锁，并发
// 发布与换池会错过彼此；少了物化，节点一直停在旧池的配置版本上。
func TestPatchAdminNodePoolMoveMaterializesUnderReleaseLock(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.PatchAdminNode")
	lockAt := strings.Index(body, `lockLegacyConfigRelease(ctx, tx, tenantID)`)
	rowAt := strings.Index(body, `FOR UPDATE`)
	updateAt := strings.Index(body, `UPDATE nodes SET`)
	syncAt := strings.Index(body, `syncLegacyDesiredConfigVersion(ctx, tx, tenantID, id)`)
	if lockAt < 0 || rowAt <= lockAt || updateAt <= rowAt || syncAt <= updateAt {
		t.Fatalf("PatchAdminNode lock/update/materialize order drifted: release=%d row=%d update=%d sync=%d",
			lockAt, rowAt, updateAt, syncAt)
	}
	if !strings.Contains(body[updateAt:], "if poolChanged {") {
		t.Fatal("desired 版本只在换池时重算")
	}
}

// 在役节点不许经 PATCH 清空池：无池节点不服务任何用户（R104）。草稿等未在役的仍可无池。
func TestPatchAdminNodeRefusesClearingPoolOfServingNode(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("Service.PatchAdminNode")
	guard := `if poolID == "" && before.ServingStatus == "active" {`
	at := strings.Index(body, guard)
	if at < 0 || !strings.Contains(body[at:], `httpx.Invalid(map[string]string{`) ||
		at > strings.Index(body, `UPDATE nodes SET`) {
		t.Fatal("PatchAdminNode 必须在写库前以 422 拒绝清空在役节点的池")
	}
}
