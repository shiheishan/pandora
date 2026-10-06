// [INPUT]: 依赖 domain/nodefabric 的节点分组用例（ListNodePools / CreateNodePool / UpdateNodePool / DeleteNodePool / CheckNodePoolAssignment）与 NotifyUsersChanged，依赖 domain/adminops 的套餐绑池 PlanPools / SetPlanPools，依赖 pool_user_groups.go 的名单校验，依赖 platform 的 db/httpx
// [OUTPUT]: 对外提供 handlers 的 listNodePools / createNodePool / updateNodePool / deleteNodePool / assignNodePool / planPools / setPlanPools（提交后发租户级 node.users.changed，R104）与 notifyNodeUsersChanged、poolActorID
// [POS]: api/admin 的节点分组：节点与套餐之间唯一的连接层；只解析与校验请求、调服务、提交后通知、写响应，不跑 SQL（分组在 nodefabric，套餐版本绑池随 plan_node_pools 的其他写入方在 adminops）

package admin

// 节点分组。
//
// 分组是「节点」和「套餐」之间唯一的连接点：节点归到分组里，
// 套餐绑定分组，用户能看到哪些节点由这两层关系决定。
//
// 在这之前这层关系只能靠手写 SQL 维护，后果是：新建的套餐一个分组都没绑，
// 卖出去之后用户拿到一份空订阅 —— 客户端里一个节点都没有，
// 而管理员那边没有任何异常提示。这是必须有界面的原因。

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func (h *handlers) listNodePools(w http.ResponseWriter, r *http.Request) {
	out, err := h.d.Node.ListNodePools(r.Context(), httpx.TenantIDFrom(r.Context()))
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listNodePoolsResponse{Pools: out})
}

type listNodePoolsResponse struct {
	Pools []nodefabric.NodePool `json:"pools"`
}

type poolReq struct {
	Code   string `json:"code"`
	Name   string `json:"name"`
	Region string `json:"region"`
	Status string `json:"status"`
	// AllowedUserGroupIDs 省略（或 null）= 不改，[] = 取消限定；带了就要近期重认证（R104）
	AllowedUserGroupIDs *[]string `json:"allowed_user_group_ids"`
}

// poolUserGroups 是 poolReq 里名单字段的校验结果：present 为 false 时不碰名单。
func (req poolReq) poolUserGroups(r *http.Request) (ids []string, present bool, err error) {
	if req.AllowedUserGroupIDs == nil {
		return nil, false, nil
	}
	if err := requirePoolGroupsReauth(r, true); err != nil {
		return nil, true, err
	}
	ids, err = normalizePoolUserGroupIDs(*req.AllowedUserGroupIDs)
	return ids, true, err
}

func (h *handlers) createNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	var req poolReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	groupIDs, withGroups, err := req.poolUserGroups(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Code = strings.TrimSpace(req.Code)
	if req.Name == "" {
		httpx.Fail(w, r, h.d.Log, httpx.Invalid(map[string]string{"name": "分组名必填"}))
		return
	}
	if req.Code == "" {
		// code 是给接口和脚本用的稳定标识，不填就从名字派生。
		// 名字全是中文时派生不出东西，退回用时间戳兜底
		req.Code = slugify(req.Name)
	}

	newID, groupsChanged, err := h.d.Node.CreateNodePool(r.Context(), tenantID, nodefabric.NodePoolInput{
		ActorID: poolActorID(r), Code: req.Code, Name: req.Name, Region: req.Region,
		WithUserGroups: withGroups, UserGroupIDs: groupIDs,
	})
	if err != nil {
		if db.IsUniqueViolation(err) {
			httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeConflict, "这个分组标识已存在"))
			return
		}
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if groupsChanged {
		h.notifyNodeUsersChanged(r)
	}
	httpx.OK(w, createNodePoolResponse{ID: newID})
}

type createNodePoolResponse struct {
	ID string `json:"id"`
}

func (h *handlers) updateNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	var req poolReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	groupIDs, withGroups, err := req.poolUserGroups(r)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if req.Status != "" && req.Status != "active" &&
		req.Status != "draining" && req.Status != "disabled" {
		httpx.Fail(w, r, h.d.Log, httpx.New(httpx.CodeValidationFailed,
			"状态只能是 active / draining / disabled"))
		return
	}
	if _, err := uuid.Parse(id); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	groupsChanged, err := h.d.Node.UpdateNodePool(r.Context(), tenantID, id, nodefabric.NodePoolInput{
		ActorID: poolActorID(r), Name: req.Name, Region: req.Region, Status: req.Status,
		WithUserGroups: withGroups, UserGroupIDs: groupIDs,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	if groupsChanged {
		h.notifyNodeUsersChanged(r)
	}
	httpx.OK(w, updateNodePoolResponse{OK: true})
}

type updateNodePoolResponse struct {
	OK bool `json:"ok"`
}

func (h *handlers) deleteNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	err := h.d.Node.DeleteNodePool(r.Context(), tenantID, poolActorID(r), id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, deleteNodePoolResponse{OK: true})
}

type deleteNodePoolResponse struct {
	OK bool `json:"ok"`
}

// assignNodePool 把一个节点归到某个分组。
func (h *handlers) assignNodePool(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	nodeID := chi.URLParam(r, "id")
	var req struct {
		PoolID string `json:"pool_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	req.PoolID = strings.TrimSpace(req.PoolID)
	if req.PoolID != "" {
		if _, err := uuid.Parse(req.PoolID); err != nil {
			httpx.Fail(w, r, h.d.Log,
				httpx.Invalid(map[string]string{"pool_id": "必须是 UUID"}))
			return
		}
	}

	err := h.d.Node.CheckNodePoolAssignment(r.Context(), tenantID, nodeID, req.PoolID)
	if errors.Is(err, nodefabric.ErrNodePoolMoveFrozen) {
		// 同池是幂等重放（nodefabric 里判定）；换池在有效发布迁移完成前一律拒绝
		err = httpx.New(httpx.CodeConflict,
			"配置发布身份升级完成前暂不允许移动节点分组")
	}
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, assignNodePoolResponse{OK: true})
}

type assignNodePoolResponse struct {
	OK bool `json:"ok"`
}

// planPools returns the mutable draft bindings when a draft exists. Without a
// draft it returns the current published snapshot as explicitly read-only.
func (h *handlers) planPools(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	planID := chi.URLParam(r, "id")
	if _, err := uuid.Parse(planID); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	out, err := h.d.Ops.PlanPools(r.Context(), tenantID, planID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, planPoolsResponse{
		VersionID: out.VersionID, VersionStatus: out.VersionStatus,
		RowVersion: out.RowVersion, Editable: out.Editable, Pools: out.Pools,
	})
}

// planPoolsResponse 是 GET v1/plans/{id}/pools 的响应；没有任何版本时 version_id 为空串。
type planPoolsResponse struct {
	VersionID     string                    `json:"version_id"`
	VersionStatus string                    `json:"version_status"`
	RowVersion    int64                     `json:"row_version"`
	Editable      bool                      `json:"editable"`
	Pools         []adminops.PlanPoolOption `json:"pools"`
}

type setPlanPoolsReq struct {
	VersionID                 string   `json:"version_id"`
	ExpectedVersionRowVersion int64    `json:"expected_version_row_version"`
	PoolIDs                   []string `json:"pool_ids"`
}

func validateSetPlanPoolsRequest(planID string, req setPlanPoolsReq) ([]string, error) {
	if _, err := uuid.Parse(planID); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	fields := map[string]string{}
	if _, err := uuid.Parse(req.VersionID); err != nil {
		fields["version_id"] = "必须是有效 UUID"
	}
	if req.ExpectedVersionRowVersion <= 0 {
		fields["expected_version_row_version"] = "必须是正整数"
	}
	if len(req.PoolIDs) > 500 {
		fields["pool_ids"] = "一次最多绑定 500 个节点分组"
	}
	poolIDs := append([]string(nil), req.PoolIDs...)
	sort.Strings(poolIDs)
	for i, id := range poolIDs {
		if _, err := uuid.Parse(id); err != nil || (i > 0 && id == poolIDs[i-1]) {
			fields["pool_ids"] = "必须是无重复的 UUID 列表"
			break
		}
	}
	if len(fields) != 0 {
		return nil, httpx.Invalid(fields)
	}
	return poolIDs, nil
}

// setPlanPools atomically replaces a draft plan version's pool bindings.
func (h *handlers) setPlanPools(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	planID := chi.URLParam(r, "id")
	var req setPlanPoolsReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	poolIDs, err := validateSetPlanPoolsRequest(planID, req)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	next, err := h.d.Ops.SetPlanPools(r.Context(), tenantID, planID, adminops.SetPlanPoolsInput{
		ActorID: httpx.PrincipalFrom(r.Context()).UserID, VersionID: req.VersionID,
		ExpectedVersionRowVersion: req.ExpectedVersionRowVersion, PoolIDs: poolIDs,
	})
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	h.notifyNodeUsersChanged(r)
	httpx.OK(w, setPlanPoolsResponse{
		Bound: len(poolIDs), RowVersion: next, VersionID: req.VersionID,
	})
}

type setPlanPoolsResponse struct {
	Bound      int    `json:"bound"`
	RowVersion int64  `json:"row_version"`
	VersionID  string `json:"version_id"`
}

// notifyNodeUsersChanged 在改变交付集合的写操作提交后，发一次租户级
// node.users.changed，让节点立即重拉用户（R104）；只在事务成功后调，
// 失败的请求什么都没改，不该惊动节点。推送尽力而为，节点端轮询兜底。
func (h *handlers) notifyNodeUsersChanged(r *http.Request) {
	if h.d.Node != nil {
		h.d.Node.NotifyUsersChanged(r.Context(), httpx.TenantIDFrom(r.Context()))
	}
}

// poolActorID 取审计里的操作人：没有主体时为空串，nodefabric 记成 NULL。
func poolActorID(r *http.Request) string {
	if a := httpx.PrincipalFrom(r.Context()); a != nil {
		return a.UserID
	}
	return ""
}

// slugify 从名字派生一个稳定标识。
//
// 中文名派生不出可读的 slug，这时退回时间戳 —— 标识只要唯一稳定就够了，
// 好不好看是次要的，而让管理员为了建个分组先想一个英文代号是多余的负担。
func slugify(name string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(name) {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == ' ' || c == '-' || c == '_':
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		buf := make([]byte, 4)
		if _, err := rand.Read(buf); err != nil {
			return "pool"
		}
		return "pool-" + hex.EncodeToString(buf)
	}
	return s
}
