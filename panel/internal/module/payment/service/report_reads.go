package service

import (
	"context"
	"time"
)

// StuckCallbackCount 数收到早于 receivedBefore、仍未处理（processing_status = pending）的支付回调；系统状态页的 payment_callbacks 用，调用方传「现在 − 1 分钟」。
func (s *Service) StuckCallbackCount(ctx context.Context, receivedBefore time.Time) (int64, error) {
	return s.repo.StuckCallbackCount(ctx, receivedBefore)
}
