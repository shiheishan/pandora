package admin

import (
	"reflect"
	"testing"
)

func TestRouteGroupRouteProtections(t *testing.T) {
	routes := loadRouteProtections(t)
	read := []string{"node.read"}
	publish := []string{"node.config.publish"}
	for key, want := range map[string]routeProtection{
		"GET /route-groups":                 {handler: "h.listRouteGroups", permissions: read},
		"POST /route-groups":                {handler: "h.createRouteGroup", permissions: publish, idempotency: "route_group_create"},
		"PATCH /route-groups/{id}":          {handler: "h.updateRouteGroup", permissions: publish, recentReauth: true, idempotency: "route_group_update"},
		"DELETE /route-groups/{id}":         {handler: "h.deleteRouteGroup", permissions: publish, recentReauth: true, idempotency: "route_group_delete"},
		"GET /route-groups/{id}/routing":    {handler: "h.getRouteGroupRouting", permissions: read},
		"PUT /route-groups/{id}/routing":    {handler: "h.setRouteGroupRouting", permissions: publish, recentReauth: true, idempotency: "route_group_routing_publish"},
		"PUT /route-groups/{id}/members":    {handler: "h.setRouteGroupMembers", permissions: publish, recentReauth: true, idempotency: "route_group_members_update"},
		"PUT /nodes/{id}/route-groups":      {handler: "h.setNodeRouteGroups", permissions: publish},
		"GET /nodes/{id}/routing/effective": {handler: "h.nodeEffectiveRouting", permissions: read},
		// 单节点路由沿用原门槛
		"GET /nodes/{id}/routing": {handler: "h.nodeGetRouting", permissions: read},
		"PUT /nodes/{id}/routing": {handler: "h.nodeSetRouting", permissions: publish},
	} {
		if got, ok := routes[key]; !ok || !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %+v (registered=%v), want %+v", key, got, ok, want)
		}
	}
}
