package admin

import (
	"reflect"
	"testing"
)

// 一步上线（R108）：与批量启用、旧状态接口同门槛（node.lifecycle，不要求重认证），
// 带幂等域 node_activate，重放不重做。
func TestNodeActivateRouteProtection(t *testing.T) {
	routes := loadRouteProtections(t)
	want := routeProtection{handler: "h.nodeActivate", permissions: []string{"node.lifecycle"},
		idempotency: "node_activate"}
	if got, ok := routes["POST /nodes/{id}/activate"]; !ok || !reflect.DeepEqual(got, want) {
		t.Fatalf("POST /nodes/{id}/activate = %+v (registered=%v), want %+v", got, ok, want)
	}
}
