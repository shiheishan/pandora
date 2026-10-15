package handler

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) issueServerBootstrapToken(c *gin.Context) {
	id, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req bootstrapTokenRequest
	if c.Request.ContentLength != 0 {
		if err := httpx.BindJSON(c, &req); err != nil {
			httpx.Fail(c, err)
			return
		}
	}
	ctx := c.Request.Context()
	out, err := h.svc.IssueBootstrapToken(ctx, id, req.TTLMinutes)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, bootstrapTokenResponseFrom(out))
}

func (h *Handler) issueNodeBootstrapToken(c *gin.Context) {
	var req nodeBootstrapTokenRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if strings.TrimSpace(req.ServerID) == "" {
		httpx.Fail(c, errs.Newf(errs.Conflict, "先把节点放到服务器上，再签发安装令牌"))
		return
	}
	sid, err := uuid.Parse(strings.TrimSpace(req.ServerID))
	if err != nil {
		httpx.Fail(c, errs.Invalid(map[string]string{"server_id": "不是合法的服务器 id"}))
		return
	}
	ctx := c.Request.Context()
	out, err := h.svc.IssueBootstrapToken(ctx, sid, req.TTLMinutes)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, bootstrapTokenResponseFrom(out))
}

func (h *Handler) nodeIdentity(c *gin.Context) {
	nodeID, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	v, err := h.svc.NodeIdentity(ctx, nodeID)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeIdentityResponseFrom(v))
}

func (h *Handler) revokeNodeIdentity(c *gin.Context) {
	nodeID, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ctx := c.Request.Context()
	if err := h.svc.RevokeNodeIdentity(ctx, nodeID); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, identityOKResponse{OK: true})
}
