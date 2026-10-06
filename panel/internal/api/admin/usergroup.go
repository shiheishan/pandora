package admin

// 用户分组。
//
// 分组本身很简单，值钱的是它能驱动的四件事：
//
//	套餐可见   visibility='group' 的套餐只对组内用户出现
//	专属价格   同一个套餐给不同组不同的价（代理价、老用户价）
//	优惠券     券可以限定只有某几个组能用
//	公告       通知只发给相关的人
//	节点池     池可以限定只有某几个组能用（R104，名单在 node_pool_user_groups）
//
// 这四处的字段在数据库里一直都在，只是没有组可填，所以全是死的。

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/domain/adminops"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

type listUserGroupsResponse struct {
	Groups []adminops.UserGroup `json:"groups"`
}

func (h *handlers) listUserGroups(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	out, err := h.d.Ops.ListUserGroups(r.Context(), tenantID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, listUserGroupsResponse{Groups: out})
}

type userGroupReq struct {
	Code string `json:"code"`
	Name string `json:"name"`
	Desc string `json:"description"`
}

type saveUserGroupResponse struct {
	ID string `json:"id"`
}

func (h *handlers) saveUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")

	var req userGroupReq
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
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
		req.Code = groupSlug(req.Name)
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	newID, err := h.d.Ops.SaveUserGroup(r.Context(), tenantID, actorID, id, req.Code, req.Name, req.Desc)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, saveUserGroupResponse{ID: newID})
}

type deleteUserGroupResponse struct {
	OK bool `json:"ok"`
}

func (h *handlers) deleteUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	id := chi.URLParam(r, "id")
	if _, err := uuid.Parse(id); err != nil {
		httpx.Fail(w, r, h.d.Log, httpx.NotFoundOrForbidden())
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Ops.DeleteUserGroup(r.Context(), tenantID, actorID, id)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	httpx.OK(w, deleteUserGroupResponse{OK: true})
}

type assignUserGroupResponse struct {
	OK bool `json:"ok"`
}

// assignUserGroup 把一个用户放进某个分组（传空则移出）。
func (h *handlers) assignUserGroup(w http.ResponseWriter, r *http.Request) {
	tenantID := httpx.TenantIDFrom(r.Context())
	userID := chi.URLParam(r, "id")
	var req struct {
		GroupID string `json:"group_id"`
	}
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}

	var actorID *string
	if a := httpx.PrincipalFrom(r.Context()); a != nil && a.UserID != "" {
		v := a.UserID
		actorID = &v
	}

	err := h.d.Ops.AssignUserGroup(r.Context(), tenantID, actorID, userID, req.GroupID)
	if err != nil {
		httpx.Fail(w, r, h.d.Log, err)
		return
	}
	// 换组会改变这个用户能用的节点池（R104），让节点立即重拉用户
	h.notifyNodeUsersChanged(r)
	httpx.OK(w, assignUserGroupResponse{OK: true})
}

func groupSlug(name string) string {
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
			return "group"
		}
		return "group-" + hex.EncodeToString(buf)
	}
	return s
}
