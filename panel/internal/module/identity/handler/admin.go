package handler

import (
	"github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/pagination"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

func (h *Handler) listUsers(c *gin.Context) {
	limit, offset, err := pagination.Parse(c, 25, 100)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var q listUsersQuery
	if err := httpx.BindQuery(c, &q); err != nil {
		httpx.Fail(c, err)
		return
	}
	f := service.UserFilter{Query: q.Q}
	if st := admSplitCSV(q.Status); len(st) > 0 {
		f.Statuses = st
	}
	switch q.GroupID {
	case "":
	case "none":
		f.NoGroup = true
	default:
		gid, parseErr := uuid.Parse(q.GroupID)
		if parseErr != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"group_id": "用户组必须是 uuid 或 none"}))
			return
		}
		f.GroupID = &gid
	}
	f.SubState = q.SubState

	ctx := c.Request.Context()
	rows, total, err := h.svc.ListUsers(ctx, f, limit, offset)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var cc admCents
	users := make([]userRowResponse, len(rows))
	for i, r := range rows {
		users[i] = admUserRowFrom(&cc, r)
	}
	if cc.err != nil {
		httpx.Fail(c, cc.err)
		return
	}
	httpx.OK(c, listUsersResponse{Users: users, Total: total})
}

func (h *Handler) getUser(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	d, err := h.svc.UserDetail(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	out, err := admUserDetailFrom(d)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, out)
}

func (h *Handler) setUserStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admSetUserStatusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.SetStatus(c.Request.Context(), id, req.Status, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admSetUserStatusResponse{OK: true, Status: req.Status})
}

func (h *Handler) resetUserPassword(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admResetUserPasswordRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.AdminResetPassword(c.Request.Context(), id, req.NewPassword, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admResetUserPasswordResponse{OK: true, SessionsRevoked: true})
}

func (h *Handler) assignUserGroup(c *gin.Context) {
	userID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admAssignUserGroupRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	var groupID *uuid.UUID
	if req.GroupID != "" {
		gid, parseErr := uuid.Parse(req.GroupID)
		if parseErr != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"group_id": "用户组必须是 uuid"}))
			return
		}
		groupID = &gid
	}
	if err := h.svc.AssignGroup(c.Request.Context(), userID, groupID); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admOKResponse{OK: true})
}

func (h *Handler) listUserGroups(c *gin.Context) {
	rows, err := h.svc.AdminListGroups(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	groups := make([]admUserGroupRow, len(rows))
	for i, g := range rows {
		groups[i] = admGroupRowFrom(g)
	}
	httpx.OK(c, admListUserGroupsResponse{Groups: groups})
}

func (h *Handler) createUserGroup(c *gin.Context) {
	var req admUserGroupBodyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	newID, err := h.svc.CreateGroup(c.Request.Context(), req.Code, req.Name, req.Description)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admCreateUserGroupResponse{ID: newID.String()})
}

func (h *Handler) updateUserGroup(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req admUserGroupBodyRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.UpdateGroup(c.Request.Context(), id, req.Name, req.Description); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admUpdateUserGroupResponse{ID: id.String()})
}

func (h *Handler) deleteUserGroup(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	if err := h.svc.DeleteGroup(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, admOKResponse{OK: true})
}
