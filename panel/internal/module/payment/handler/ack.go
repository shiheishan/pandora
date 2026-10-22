package handler

import (
	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/gin-gonic/gin"
)

// 本文件是支付回调应答的非 JSON 出口，forbidigo 按文件放行（白名单在 tools/depguardgen）；
// 同包别的文件不直接写响应，经 httpx。

// writeAck 原样回渠道要求的应答（状态码、类型与正文由渠道 provider 定，渠道不解析我们的信封）。
func writeAck(c *gin.Context, ack provider.AckOutput) {
	c.Data(ack.HTTPStatus, ack.ContentType, ack.Body)
}
