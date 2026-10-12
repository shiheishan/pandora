package handler

import (
	"time"

	"github.com/aegispanel/aegis/internal/module/catalog/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// packRow 与旧 adminops.TrafficPackRow JSON 一致。
type packRow struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	TrafficBytes int64     `json:"traffic_bytes"`
	Currency     string    `json:"currency"`
	UnitAmount   int64     `json:"unit_amount"`
	Recommended  bool      `json:"recommended"`
	Status       string    `json:"status"`
	SortOrder    int       `json:"sort_order"`
	SoldCount    int64     `json:"sold_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

type packRequest struct {
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
	Name              string     `json:"name"`
	TrafficBytes      int64      `json:"traffic_bytes"`
	Currency          string     `json:"currency"`
	UnitAmount        int64      `json:"unit_amount"`
	Recommended       bool       `json:"recommended"`
	SortOrder         int        `json:"sort_order"`
}

type listTrafficPacksResponse struct {
	Packs []packRow `json:"packs"`
}

type createTrafficPackResponse struct {
	Pack packRow `json:"pack"`
}

type updateTrafficPackResponse struct {
	Pack packRow `json:"pack"`
}

type setTrafficPackStatusResponse struct {
	Pack packRow `json:"pack"`
}

type packStatusRequest struct {
	Status            string     `json:"status"`
	ExpectedUpdatedAt *time.Time `json:"expected_updated_at"`
}

func packInputFrom(req packRequest) service.PackInput {
	return service.PackInput{
		Name:         req.Name,
		TrafficBytes: req.TrafficBytes,
		Currency:     req.Currency,
		UnitAmount:   money.FromCents(req.UnitAmount),
		Recommended:  req.Recommended,
		SortOrder:    req.SortOrder,
	}
}

func packRowFrom(p service.TrafficPack, sold int64) packRow {
	return packRow{
		ID:           p.ID.String(),
		Name:         p.Name,
		TrafficBytes: p.TrafficBytes,
		Currency:     string(p.Currency),
		UnitAmount:   money.MustCents(p.UnitAmount),
		Recommended:  p.Recommended,
		Status:       p.Status,
		SortOrder:    p.SortOrder,
		SoldCount:    sold,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
}

func packSoldCounts(ctx *gin.Context, h *Handler, ids []uuid.UUID) (map[uuid.UUID]int64, error) {
	if h.sales == nil || len(ids) == 0 {
		return nil, nil
	}
	return h.sales.PackSoldCounts(ctx.Request.Context(), ids)
}

func packRowsFrom(ctx *gin.Context, h *Handler, packs []service.TrafficPack) ([]packRow, error) {
	ids := make([]uuid.UUID, len(packs))
	for i, p := range packs {
		ids[i] = p.ID
	}
	counts, err := packSoldCounts(ctx, h, ids)
	if err != nil {
		return nil, err
	}
	rows := make([]packRow, len(packs))
	for i, p := range packs {
		var sold int64
		if counts != nil {
			sold = counts[p.ID]
		}
		rows[i] = packRowFrom(p, sold)
	}
	return rows, nil
}

func (h *Handler) listTrafficPacks(c *gin.Context) {
	status := c.Query("status")
	packs, err := h.svc.ListTrafficPacks(c.Request.Context(), status)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := packRowsFrom(c, h, packs)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if rows == nil {
		rows = []packRow{}
	}
	httpx.OK(c, listTrafficPacksResponse{Packs: rows})
}

func (h *Handler) createTrafficPack(c *gin.Context) {
	var req packRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := packInputFrom(req)
	pack, err := h.svc.CreateTrafficPack(c.Request.Context(), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := packRowsFrom(c, h, []service.TrafficPack{pack})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.Created(c, createTrafficPackResponse{Pack: rows[0]})
}

func (h *Handler) updateTrafficPack(c *gin.Context) {
	packID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req packRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	in := packInputFrom(req)
	pack, err := h.svc.UpdateTrafficPack(c.Request.Context(), packID, req.ExpectedUpdatedAt, in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := packRowsFrom(c, h, []service.TrafficPack{pack})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, updateTrafficPackResponse{Pack: rows[0]})
}

func (h *Handler) setTrafficPackStatus(c *gin.Context) {
	packID, err := uuid.Parse(c.Param("id"))
	if err != nil {
		httpx.Fail(c, errs.New(errs.NotFound))
		return
	}
	var req packStatusRequest
	if err := httpx.BindJSON(c, &req); err != nil {
		httpx.Fail(c, err)
		return
	}
	pack, err := h.svc.SetTrafficPackStatus(c.Request.Context(), packID, req.Status, req.ExpectedUpdatedAt)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	rows, err := packRowsFrom(c, h, []service.TrafficPack{pack})
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	httpx.OK(c, setTrafficPackStatusResponse{Pack: rows[0]})
}
