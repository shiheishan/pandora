package handler

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/aegispanel/aegis/internal/pkg/httpx"
)

// defaultMetricsMinutes 是 minutes 缺省或不是整数时取的窗口（与旧接口相同，不报错）。
const defaultMetricsMinutes = 60

// nodeMetrics 返回节点所在服务器最近 minutes 分钟的指标曲线（GET /v1/nodes/:id/metrics?minutes=）。
func (h *Handler) nodeMetrics(c *gin.Context) {
	id, err := poolPathID(c, "id")
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	minutes, err := strconv.Atoi(c.Query("minutes"))
	if err != nil {
		minutes = defaultMetricsMinutes
	}
	m, err := h.svc.NodeMetrics(c.Request.Context(), id, minutes)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, metricsResponseFrom(m))
}
