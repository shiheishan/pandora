package handler

import (
	"io"
	"strconv"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/aegispanel/aegis/internal/pkg/ratelimit"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

// maxQuoteAmountCents 是支付方式报价接受的最大金额（1 亿元，单位分）：再大算合计会溢出分的 int64 换算，回 422 而不是 500。
const maxQuoteAmountCents = 10_000_000_000

// paymentMethods 列出门户可用的支付方式。
func (h *Handler) paymentMethods(c *gin.Context) {
	var amountPtr *decimal.Decimal
	q := c.Query("amount")
	if q != "" {
		n, err := strconv.ParseInt(q, 10, 64)
		if err != nil || n < 0 || n > maxQuoteAmountCents {
			httpx.Fail(c, errs.Invalid(map[string]string{
				"amount": "金额需为 0 到 1 亿元之间的整数（分）",
			}))
			return
		}
		if n > 0 {
			d := money.FromCents(n)
			amountPtr = &d
		}
	}
	rows, err := h.svc.PaymentMethods(c.Request.Context(), amountPtr)
	if err != nil {
		httpx.Fail(c, err)
		return
	}
	methods := make([]paymentMethodResponse, 0, len(rows))
	for _, row := range rows {
		m, err := paymentMethodFrom(row)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		methods = append(methods, m)
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
