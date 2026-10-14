package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/crypto"
)

// LoadOrCreateCA 在 serve 启动时加载或创建节点 CA。
func (s *Service) LoadOrCreateCA(ctx context.Context, sealer *crypto.Sealer) (*CA, error) {
	row, err := s.repo.LoadCA(ctx)
	if err != nil {
		return nil, err
	}
	if row == nil {
		ca, keyDER, err := generateCA(s.now())
		if err != nil {
			return nil, err
		}
		sealed, err := sealer.Seal(keyDER, []byte(caKeyAAD))
		clear(keyDER)
		if err != nil {
			return nil, err
		}
		if err := s.repo.InsertCAIfAbsent(ctx, &model.NodeCA{
			ID:         1,
			CertPEM:    string(ca.certPEM),
			KeySealed:  sealed,
			SPKISHA256: spkiSHA256Hex(ca.cert),
			CreatedAt:  s.now(),
		}); err != nil {
			return nil, err
		}
		row, err = s.repo.LoadCA(ctx)
		if err != nil {
			return nil, err
		}
		if row == nil {
			return nil, errors.New("node: 创建 CA 后仍读不到行")
		}
	}

	keyDER, err := sealer.Open(row.KeySealed, []byte(caKeyAAD))
	if err != nil {
		return nil, errors.New("node: 打开 CA 私钥失败（主密钥变了？）")
	}
	ca, err := parseCA([]byte(row.CertPEM), keyDER)
	clear(keyDER)
	if err != nil {
		return nil, err
	}
	if spkiSHA256Hex(ca.cert) != row.SPKISHA256 {
		return nil, fmt.Errorf("node: 库中 CA 指纹与证书不一致")
	}
	return ca, nil
}
