package nodefabric

import (
	"os"
	"strings"
	"testing"
)

// 上线路径只能走 00005 node_transitions 里的边，而且每条都以 active 结尾；
// 进正式池的唯一入口是 canary → active（NODE-010）。
func TestActivatePathsFollowNodeTransitions(t *testing.T) {
	migration, err := os.ReadFile("../../../migrations/00005_node_fabric.sql")
	if err != nil {
		t.Fatal(err)
	}
	edges := string(migration)
	for from, path := range activatePaths {
		if len(path) == 0 || path[len(path)-1] != "active" {
			t.Fatalf("%s path %v does not end at active", from, path)
		}
		if len(path) < 1 || (len(path) >= 2 && path[len(path)-2] != "canary") || (len(path) == 1 && from != "canary") {
			t.Fatalf("%s path %v must enter active from canary", from, path)
		}
		prev := from
		for _, next := range path {
			if !strings.Contains(edges, "('"+prev+"', '"+next+"')") {
				t.Fatalf("%s path uses %s -> %s, not a node_transitions edge", from, prev, next)
			}
			prev = next
		}
	}
	for _, refused := range []string{"draft", "bootstrapping", "bootstrap_failed", "quarantined",
		"retired", "destroyed", "draining", "maintenance", "unhealthy", "upgrade_failed", "active"} {
		if _, ok := activatePaths[refused]; ok {
			t.Fatalf("%s must not be activatable in one step", refused)
		}
		if refused != "active" && activateRefusal(refused) == "" {
			t.Fatalf("%s has no refusal message", refused)
		}
	}
	if serving, server := ProjectNodeLifecycle("active", true); serving != "active" || server != "ready" {
		t.Fatalf("active projection = %s/%s", serving, server)
	}
}
