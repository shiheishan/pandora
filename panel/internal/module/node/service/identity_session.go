package service

// 会话的身份校验与续期（p5-enroll 实现）。

import (
	"context"
	"errors"
	"math/big"

	"github.com/google/uuid"
)

// ErrSessionRevoked 是建流时身份已无效；会话层先发 Command.tombstone 再结束流。
var ErrSessionRevoked = errors.New("node: 接入身份已失效")

// AuthorizeSession 在每次建流时调：客户端证书（已由 TLS 层按面板 CA 验过链）的序列号必须是这台服务器当前有效的身份。
// 无效（吊销、过期、被新身份取代、服务器已删）回 ErrSessionRevoked。
func (s *Service) AuthorizeSession(ctx context.Context, serverID uuid.UUID, certSerial *big.Int) error {
	panic("p5-enroll: AuthorizeSession")
}

// RenewCertificate 用会话里的 CSR 给这台服务器签一张新证书（新私钥由节点生成），登记为新的有效身份、旧身份作废。
// 返回 PEM 证书链（叶子在前）与 CA bundle。
func (s *Service) RenewCertificate(ctx context.Context, serverID uuid.UUID, csrDER []byte) (chainPEM, caPEM []byte, err error) {
	panic("p5-enroll: RenewCertificate")
}
