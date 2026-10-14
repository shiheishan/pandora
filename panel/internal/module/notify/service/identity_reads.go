package service

import (
	"context"

	identitysvc "github.com/aegispanel/aegis/internal/module/identity/service"
	"github.com/google/uuid"
)

var _ identitysvc.OpsTelegram = (*Service)(nil)

// AdminTelegramBinding 返回用户的 Telegram 绑定（后台用户详情用）；没绑定为 nil。不看站点是否启用 Telegram。
func (s *Service) AdminTelegramBinding(ctx context.Context, userID uuid.UUID) (*identitysvc.TelegramRef, error) {
	b, ok, err := s.store.Binding(ctx, userID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	return &identitysvc.TelegramRef{Username: b.Username, BoundAt: b.BoundAt}, nil
}
