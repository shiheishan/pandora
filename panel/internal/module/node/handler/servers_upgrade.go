package handler

// 批量在线升级接口：POST /v1/servers/upgrade。

import (
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

type serverUpgradeRequest struct {
	ServerIDs []string `json:"server_ids"`
}

type serverUpgradeResultResponse struct {
	ServerID uuid.UUID `json:"server_id"`
	OK       bool      `json:"ok"`
	Reason   *string   `json:"reason"` // 已下发时 null，否则是原因码
}

type serverUpgradeResponse struct {
	TargetVersion string                        `json:"target_version"`
	Results       []serverUpgradeResultResponse `json:"results"` // 恒为数组，不为 null
}

func serverUpgradeFrom(b service.UpgradeBatch) serverUpgradeResponse {
	out := make([]serverUpgradeResultResponse, 0, len(b.Results))
	for _, r := range b.Results {
		out = append(out, serverUpgradeResultResponse{
			ServerID: r.ServerID,
			OK:       r.OK,
			Reason:   optString(r.Reason),
		})
	}
	return serverUpgradeResponse{TargetVersion: b.TargetVersion, Results: out}
}

func (h *Handler) upgradeServers(c *gin.Context) {
	var req serverUpgradeRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ids := make([]uuid.UUID, 0, len(req.ServerIDs))
	for _, raw := range req.ServerIDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"server_ids": "服务器 ID 格式不正确"}))
			return
		}
		ids = append(ids, id)
	}
	batch, err := h.svc.RequestUpgrades(c.Request.Context(), ids)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, serverUpgradeFrom(batch))
}
