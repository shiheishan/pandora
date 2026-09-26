// [INPUT]: 依赖 Service 的预览与轮换入口，依赖 platform/sourcetest 按名取节点列表、预览、共用资格查询与订阅链接的源码
// [OUTPUT]: 对外提供 TestNodePreviewCannotCarryConnectionSecrets、TestInvalidSubscriptionPreviewIDIsNeutralNotFound、TestInvalidSubscriptionRotationIDIsNeutralNotFound、TestSubscriptionAndPreviewShareOneEligibilityQuery
// [POS]: subscription 订阅与节点预览共用一个资格查询，协议可用与池准入谓词只出现在那里，预览不带连接密钥
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package subscription

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestNodePreviewCannotCarryConnectionSecrets(t *testing.T) {
	typeOf := reflect.TypeOf(NodePreview{})
	want := []string{"Name", "Protocol", "TrafficRate"}
	if typeOf.NumField() != len(want) {
		t.Fatalf("NodePreview has %d fields, want exactly %d safe fields", typeOf.NumField(), len(want))
	}
	for index, name := range want {
		if typeOf.Field(index).Name != name {
			t.Fatalf("NodePreview field %d = %s, want %s", index, typeOf.Field(index).Name, name)
		}
	}
}

func TestInvalidSubscriptionPreviewIDIsNeutralNotFound(t *testing.T) {
	service := &Service{}
	_, err := service.ListOwnedNodePreviews(context.Background(), "tenant", "user", "not-a-uuid")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid subscription ID error = %v, want ErrNotFound", err)
	}
}

func TestInvalidSubscriptionRotationIDIsNeutralNotFound(t *testing.T) {
	service := &Service{}
	_, err := service.Rotate(context.Background(), "tenant", "user", "not-a-uuid")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid rotation ID error = %v, want ErrNotFound", err)
	}
}

func TestSubscriptionAndPreviewShareOneEligibilityQuery(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	body := pkg.Source()
	listNodes := pkg.Decl("Service.ListNodes")
	// 原窗口从 ListOwnedNodePreviews 到 listEligibleNodesTx，中间夹着 HeartbeatFreshWindow 与 DeliveryState
	owned := pkg.Decls("Service.ListOwnedNodePreviews", "HeartbeatFreshWindow", "DeliveryState")
	// 原窗口从 listEligibleNodesTx 到 LoadUsage，中间夹着 preferFreshNodes
	eligible := pkg.Decls("listEligibleNodesTx", "preferFreshNodes")
	links := pkg.Decl("Service.ListLinks")
	for name, function := range map[string]string{"ListNodes": listNodes, "ListOwnedNodePreviews": owned} {
		if strings.Count(function, "listEligibleNodesTx(") != 1 || strings.Contains(function, "FROM nodes") {
			t.Fatalf("%s must call the shared eligibility query exactly once", name)
		}
	}
	if strings.Count(eligible, `nodefabric.StableProtocolReadySQL("n")`) != 1 ||
		strings.Count(body, `nodefabric.StableProtocolReadySQL("n")`) != 1 {
		t.Fatal("stable protocol qualification must exist only in the shared query")
	}
	// 池限定用户组（R104）同理：谓词只出现在共用查询里，且带订阅主人。
	if strings.Count(eligible, `nodefabric.PoolAdmitsUserSQL("n.tenant_id", "n.pool_id", "$4::uuid")`) != 1 ||
		strings.Count(body, `nodefabric.PoolAdmitsUserSQL(`) != 1 {
		t.Fatal("pool user-group admission must exist only in the shared query")
	}
	if !strings.Contains(listNodes, `listEligibleNodesTx(ctx, tx, tenantID, c.UserID, c.PlanVersionID)`) ||
		!strings.Contains(owned, `listEligibleNodesTx(ctx, tx, tenantID, userID, planVersionID)`) {
		t.Fatal("both callers must pass the subscription owner into the shared query")
	}
	for _, want := range []string{
		`db.Scope{TenantID: tenantID, ActorID: userID}`,
		`AND s.status IN ('active','trialing','grace')`,
		`AND EXISTS (`,
		`sc.status='active'`,
		`(sc.expires_at IS NULL AND sc.grace_until IS NULL)`,
		`GREATEST(sc.expires_at,sc.grace_until) > now()`,
		`if errors.Is(err, pgx.ErrNoRows)`,
		`return ErrNotFound`,
	} {
		if !strings.Contains(owned, want) {
			t.Fatalf("owned node preview contract missing %q", want)
		}
	}
	for _, want := range []string{
		`(sc.expires_at IS NULL AND sc.grace_until IS NULL)`,
		`GREATEST(sc.expires_at, sc.grace_until) > now()`,
	} {
		if !strings.Contains(links, want) {
			t.Fatalf("subscription link deadline contract missing %q", want)
		}
	}
}
