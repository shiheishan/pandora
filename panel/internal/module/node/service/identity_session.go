package service

// 会话的身份校验与续期（p5-enroll 实现）。

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
)

// ErrSessionRevoked 是建流时身份已无效；会话层先发 Command.tombstone 再结束流。
var ErrSessionRevoked = errors.New("node: 接入身份已失效")

// identityNow 是身份相关写库用的当前时间（UTC，截到微秒，与 timestamptz 精度一致）。
func (s *Service) identityNow() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// AuthorizeSession 在每次建流时调：客户端证书（已由 TLS 层按面板 CA 验过链）的序列号必须是这台服务器当前有效的身份。
// 无效（吊销、过期、被新身份取代、服务器已删）回 ErrSessionRevoked。
func (s *Service) AuthorizeSession(ctx context.Context, serverID uuid.UUID, certSerial *big.Int) error {
	if certSerial == nil || certSerial.Sign() <= 0 {
		return ErrSessionRevoked
	}
	ok, err := s.repo.SessionIdentityValid(ctx, serverID, certSerial.Text(16), s.identityNow())
	if err != nil {
		return err
	}
	if !ok {
		return ErrSessionRevoked
	}
	return nil
}

// RenewCertificate 用会话里的 CSR 给这台服务器签一张新证书（新私钥由节点生成），登记为新的有效身份、旧身份作废。
// 返回 PEM 证书链（叶子在前）与 CA bundle。续期不断当前流：下次建流用新证书。
func (s *Service) RenewCertificate(ctx context.Context, serverID uuid.UUID, csrDER []byte) (chainPEM, caPEM []byte, err error) {
	ca := s.enroll.ca
	if ca == nil {
		return nil, nil, errCANotLoaded
	}
	pub, csrHash, err := parseNodeCSR(csrDER)
	if err != nil {
		return nil, nil, err
	}
	now := s.identityNow()
	var leafPEM []byte
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		st, err := s.repo.LockServerForIdentity(ctx, serverID)
		if err != nil {
			return err
		}
		if st == nil || st.Deleted {
			return ErrSessionRevoked
		}
		oldID, ok, err := s.repo.SupersedeActiveIdentity(ctx, serverID, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSessionRevoked
		}
		row, err := s.issueIdentity(ctx, ca, serverID, pub, csrHash, now, nil, nil)
		if err != nil {
			return err
		}
		leafPEM = []byte(row.CertPEM)
		return s.audit.Write(ctx, auditsvc.Event{
			Action:       "server.identity.renew",
			ResourceType: "server",
			ResourceID:   &serverID,
			Data: map[string]any{
				"identity_id":            row.ID.String(),
				"seq":                    row.Seq,
				"superseded_identity_id": oldID.String(),
			},
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return certChainPEM(ca, leafPEM), ca.CertPEM(), nil
}

// issueIdentity 签证书并写入一份有效身份（调用方已锁住服务器行、已把旧的有效身份置为 superseded）。
// tokenID、requestID 只有接入时有。
func (s *Service) issueIdentity(ctx context.Context, ca *CA, serverID uuid.UUID, pub *ecdsa.PublicKey, csrHash string,
	now time.Time, tokenID *uuid.UUID, requestID *string) (*model.ServerIdentity, error) {
	seq, err := s.repo.NextIdentitySeq(ctx, serverID)
	if err != nil {
		return nil, err
	}
	iss, err := ca.issueClientCert(pub, serverID, now)
	if err != nil {
		return nil, err
	}
	source := "renew"
	if tokenID != nil {
		source = "enroll"
	}
	row := &model.ServerIdentity{
		ServerID:        serverID,
		Seq:             seq,
		CertSerial:      iss.SerialHex,
		SPKISHA256:      iss.SPKISHA256,
		CertPEM:         string(iss.PEM),
		CSRSHA256:       csrHash,
		Source:          source,
		EnrollTokenID:   tokenID,
		EnrollRequestID: requestID,
		Status:          "active",
		IssuedAt:        now,
		ExpiresAt:       iss.Cert.NotAfter.UTC(),
	}
	if err := s.repo.InsertIdentity(ctx, row); err != nil {
		return nil, err
	}
	return row, nil
}
