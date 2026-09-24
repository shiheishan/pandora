// [INPUT]: 依赖 router.go 的 Deps 与 NewRouter 里已挂 RequireAuth 的 /v1 分组，依赖 middleware 的权限/重认证/幂等链
// [OUTPUT]: 对外提供 registerNodePoolRoutes、registerNodeRoutes、nodeBatchStatusIdempotencyScope
// [POS]: api/admin 路由表的「节点分组与套餐绑定、节点、服务器、令牌签发、身份吊销、配置发布、路由」段，由 NewRouter 按原注册顺序调用；处理器在 pools.go / node_admin.go / server.go / handlers.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"github.com/go-chi/chi/v5"

	"github.com/aegispanel/aegis/internal/middleware"
)

// nodeBatchStatusIdempotencyScope 由批量改节点状态的两条路由共用。
const nodeBatchStatusIdempotencyScope = "node_status_batch"

func registerNodePoolRoutes(r chi.Router, d Deps, h *handlers) {
	// 节点分组：节点与套餐之间的连接层
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/node-pools", h.listNodePools)
	r.With(middleware.RequirePermission("node.provision", d.Log)).
		Post("/node-pools", h.createNodePool)
	r.With(middleware.RequirePermission("node.provision", d.Log)).
		Post("/node-pools/{id}", h.updateNodePool)
	r.With(middleware.RequirePermission("node.provision", d.Log)).
		Delete("/node-pools/{id}", h.deleteNodePool)
	r.With(middleware.RequirePermission("catalog.read", d.Log)).
		Get("/plans/{id}/pools", h.planPools)
	r.With(
		middleware.RequirePermission("catalog.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "catalog_plan_pools_update", d.Log),
	).Post("/plans/{id}/pools", h.setPlanPools)
}

func registerNodeRoutes(r chi.Router, d Deps, h *handlers) {
	// --- 节点（NODE / AGT）---
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/nodes", h.nodeList)
	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.Idempotency(d.Pool, "node_create", d.Log),
	).Post("/nodes", h.createAdminNode)
	r.With(middleware.RequirePermission("node.write", d.Log)).
		Patch("/nodes/{id}", h.patchAdminNode)
	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.Idempotency(d.Pool, "node_copy", d.Log),
	).Post("/nodes/{id}/copy", h.copyAdminNode)
	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.Idempotency(d.Pool, "node_server_move", d.Log),
	).Post("/nodes/{id}/move", h.moveAdminNode)
	r.With(
		middleware.RequirePermission("node.write", d.Log),
	).Put("/nodes/order", h.reorderAdminNodes)
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.Idempotency(d.Pool, nodeBatchStatusIdempotencyScope, d.Log),
	).Post("/nodes/status:batch", h.batchAdminNodeStatus)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/node-protocol-schemas", h.nodeProtocolSchemas)

	// --- Server 物理宿主 ---
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/servers", h.serverList)
	r.With(
		middleware.RequirePermission("node.write", d.Log),
	).Post("/servers", h.serverCreate)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/servers/{id}", h.serverGet)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/servers/{id}/nodes", h.serverNodes)
	r.With(middleware.RequirePermission("node.write", d.Log)).
		Patch("/servers/{id}", h.serverPatch)
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
	).Post("/servers/{id}/status", h.serverSetStatus)
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/servers/{id}", h.serverDelete)
	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "node_bootstrap_token_issue", d.Log),
	).Post("/nodes/bootstrap-token", h.nodeIssueToken)

	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "server_bootstrap_token_issue", d.Log),
	).Post("/servers/{id}/bootstrap-token", h.serverIssueToken)

	// REALITY 密钥对生成。只读权限就能调：它不碰任何现存数据，
	// 生成一对没人用的密钥本身不构成风险，而把它锁在写权限后面
	// 只会让「先生成看看」这个自然动作变得别扭。
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Post("/nodes/reality-keypair", h.nodeRealityKeypair)

	// 删除节点。要 lifecycle 权限 + 近期重认证：它会连带删掉这个节点
	// 全部的指标与流量上报（外键是 CASCADE），撤不回来。
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Delete("/nodes/{id}", h.nodeDelete)
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.Idempotency(d.Pool, "node_legacy_status", d.Log),
	).
		Post("/nodes/{id}/status", h.nodeSetStatus)

	// --- 节点批量操作 ---
	// batch/status 是 status:batch 的别名，同一个处理器、同一个幂等 scope：
	// scope 不同的话，同一个 Idempotency-Key 换条路径就会再执行一次。
	// retired 就是这个模型里的「删除」：节点有历史，不做物理删除。
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.Idempotency(d.Pool, nodeBatchStatusIdempotencyScope, d.Log),
	).Post("/nodes/batch/status", h.batchAdminNodeStatus)
	// 没有批量移动：单节点移动要求节点停用且不带任何 agent 资产
	// （身份、指标、任务、流量上报、有效令牌…），也就是只有从没用过的
	// 草稿节点能移。批量化一个「几乎总是被拒绝」的操作没有意义，
	// 而复制那套守卫必然和单节点路径分叉。
	// 一步退役：不可逆，要重认证；幂等防重试把已退役节点再退一次拿到 409
	r.With(
		middleware.RequirePermission("node.lifecycle", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "node_retire", d.Log),
	).Post("/nodes/{id}/retire", h.nodeRetire)
	r.With(
		middleware.RequirePermission("node.identity.revoke", d.Log),
		middleware.RequireRecentReauth(d.Log),
	).Post("/nodes/{id}/revoke-identity", h.nodeRevokeIdentity)
	r.With(
		middleware.RequirePermission("node.config.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "node_config_publish", d.Log),
	).Post("/nodes/config/publish", h.nodePublishConfig)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/nodes/{id}/metrics", h.nodeMetrics)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/nodes/{id}/identity", h.nodeIdentity)
	r.With(middleware.RequirePermission("node.write", d.Log)).
		Post("/nodes/{id}/protocol", h.nodeSetProtocol)
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/nodes/{id}/routing", h.nodeGetRouting)
	r.With(middleware.RequirePermission("node.config.publish", d.Log)).
		Put("/nodes/{id}/routing", h.nodeSetRouting)
	// 全局出站与分流：静态段 routing 优先于 /nodes/{id}；发布影响全部节点，
	// 与单节点发布同级要重认证，幂等防网络重试把全部节点的 generation 推两次
	r.With(middleware.RequirePermission("node.read", d.Log)).
		Get("/nodes/routing", h.nodeGetGlobalRouting)
	r.With(
		middleware.RequirePermission("node.config.publish", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "node_routing_global_publish", d.Log),
	).Put("/nodes/routing", h.nodeSetGlobalRouting)
	r.With(
		middleware.RequirePermission("node.provision", d.Log),
		middleware.RequireRecentReauth(d.Log),
		middleware.Idempotency(d.Pool, "node_server_token_issue", d.Log),
	).Post("/nodes/{id}/server-token", h.nodeIssueServerToken)
}
