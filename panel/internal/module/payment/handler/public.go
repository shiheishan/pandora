package handler

import (
	"io"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/gin-gonic/gin"
)

// paymentMethods 列出门户可用的支付方式。
func (h *Handler) paymentMethods(c *gin.Context) {
	rows, err := h.svc.PaymentMethods(c.Request.Context())
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	methods := make([]paymentMethodResponse, 0, len(rows))
	for _, row := range rows {
		methods = append(methods, paymentMethodFrom(row))
	}
	httpx.OK(c, paymentMethodsResponse{Methods: methods})
}

// paymentWebhook 接收支付渠道回调，回执由 service 按渠道原样输出。
func (h *Handler) paymentWebhook(c *gin.Context) {
	if h.lim != nil {
		if ip, ok := ratelimit.ByIP(c); ok {
			if err := h.lim.Allow(c.Request.Context(), ratelimit.PerMinute("payment_webhook_ip", WebhookPerIPPerMin), ip); err != nil {
				httpx.Fail(c, err)
				return
			}
		}
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, httpx.MaxBodyBytes+1))
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	if len(body) > httpx.MaxBodyBytes {
		httpx.Fail(c, errs.Newf(errs.BadRequest, "回调报文过大"))
		return
	}
	in := provider.Inbound{
		Method: c.Request.Method,
		Query:  c.Request.URL.Query(),
		Header: c.Request.Header.Clone(),
		Body:   body,
	}
	ack, err := h.svc.HandleWebhook(c.Request.Context(), c.Param("provider"), in)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	c.Data(ack.HTTPStatus, ack.ContentType, ack.Body)
}
