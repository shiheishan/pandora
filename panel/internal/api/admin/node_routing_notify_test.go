package admin

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestNodeSetRoutingNotifiesNodeAfterCommit(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("handlers.nodeSetRouting")
	save := strings.Index(body, "h.d.Node.SetNodeRouting(")
	failed := strings.LastIndex(body, "httpx.Fail(w, r, h.d.Log, err)")
	notify := strings.Index(body, "h.d.Node.NotifyNodeChanged(r.Context(), tenantID, id)")
	if save < 0 || notify < 0 {
		t.Fatal("nodeSetRouting must save through nodefabric and notify the node afterwards")
	}
	if notify < failed || notify < save {
		t.Fatal("NotifyNodeChanged must come after the save's error check, i.e. after commit")
	}
}
