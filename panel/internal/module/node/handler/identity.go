package handler

import (
	"github.com/gin-gonic/gin"

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
