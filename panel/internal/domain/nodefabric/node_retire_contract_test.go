package nodefabric

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 「控制节点能不能退」只有一份口径：单个退役、批量退役、删除都经 controlsLiveServerSQL，
// 且口径只数在役服务器。以前批量那条数全部服务器，接入过的节点（都是自己服务器的控制节点）
// 经 status:batch 永远退不掉（10k-r1 实测）。
func TestControlNodeDependencyHasOneDefinition(t *testing.T) {
	frag := controlsLiveServerSQL("n")
	for _, needle := range []string{
		`s.tenant_id = n.tenant_id AND s.control_node_id = n.id`,
		`s.deleted_at IS NULL`,
		`s.status NOT IN ('retired','destroyed')`,
	} {
		if !strings.Contains(frag, needle) {
			t.Fatalf("control-node predicate lacks %q:\n%s", needle, frag)
		}
	}
	pkg := sourcetest.Load(t, ".")
	for _, decl := range []string{"Service.RetireNode", "Service.BatchAdminNodeLifecycle", "Service.DeleteNode"} {
		block := pkg.Decl(decl)
		if !strings.Contains(block, `controlsLiveServerSQL("n")`) {
			t.Fatalf("%s no longer uses the shared control-node predicate", decl)
		}
		// 不许再各写一份：直接按 control_node_id 找服务器的写法会和口径漂移（批量启用的就绪判断
		// 「s.control_node_id IS DISTINCT FROM n.id」是另一回事，不在此列）
		for _, own := range []string{"control_node_id=$2", "control_node_id = $2", "s.control_node_id = n.id"} {
			if strings.Contains(block, own) {
				t.Fatalf("%s carries its own control-node check %q next to the shared predicate", decl, own)
			}
		}
	}
	// 批量退役仍逐项检查身份与任务，不因放开控制面而削弱
	batch := pkg.Decl("Service.BatchAdminNodeLifecycle")
	for _, needle := range []string{
		`node_identities WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active'`,
		`node_tasks WHERE tenant_id=$1 AND node_id=$2::uuid AND status IN ('pending','dispatched','running')`,
		`errControlsLiveServer()`,
	} {
		if !strings.Contains(batch, needle) {
			t.Fatalf("batch retirement lost dependency check %q", needle)
		}
	}
}
