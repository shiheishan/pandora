package handler

// 节点流量重置、批量修改、ECH 的后台接口。

import (
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/node/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

func (h *Handler) resetNodeTraffic(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeTrafficResetRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.ResetNodeTraffic(c.Request.Context(), id, req.Reason); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeOKResponse{OK: true})
}

func (h *Handler) batchEditNodes(c *gin.Context) {
	var req nodeBatchEditRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	items := make([]service.NodeVersionRef, 0, len(req.Items))
	for _, it := range req.Items {
		id, err := uuid.Parse(strings.TrimSpace(it.ID))
		if err != nil {
			httpx.Fail(c, errs.Invalid(map[string]string{"items": "节点 ID 格式不正确"}))
			return
		}
		items = append(items, service.NodeVersionRef{ID: id, RowVersion: it.RowVersion})
	}
	set := nodeBatchFieldsToService(req.Set)
	if req.Set.PoolID.Set {
		set.PoolID.Set = true
		if req.Set.PoolID.Value != nil {
			pool, err := nodeParseOptionalID("pool_id", *req.Set.PoolID.Value)
			if err != nil {
				httpx.Fail(c, err)
				return
			}
			set.PoolID.Value = pool
		}
	}
	n, err := h.svc.BatchEditNodes(c.Request.Context(), service.NodeBatchEdit{
		Items:  items,
		Set:    set,
		Reason: req.Reason,
	})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeOKUpdatedResponse{OK: true, Updated: n})
}

func (h *Handler) getNodeECH(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	ech, err := h.svc.GetNodeECH(c.Request.Context(), id)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, echFrom(ech))
}

func (h *Handler) generateNodeECH(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	var req nodeECHGenerateRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	ech, err := h.svc.GenerateNodeECH(c.Request.Context(), id, req.PublicName)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, echFrom(ech))
}

func (h *Handler) disableNodeECH(c *gin.Context) {
	id, err := inventoryPathID(c)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if err := h.svc.DisableNodeECH(c.Request.Context(), id); err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, nodeOKResponse{OK: true})
}
