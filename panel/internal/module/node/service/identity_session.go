package service

// 会话的身份校验与续期。
//
// 续期分两步：RenewCertificate 签出的新证书先登记为 pending，旧的 active 照常有效；节点第一次用新证书建流时
// AuthorizeSession 在同一事务里把 pending 转为 active、旧的 active 置 superseded。节点没收到新证书、或落盘前
// 崩溃，下次仍用旧证书连上，不会被锁死。

import (
	"context"
	"crypto/ecdsa"
	"errors"
	"math/big"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
)

// ErrSessionRevoked 是建流时身份已无效；会话层先发 Command.tombstone 再结束流。
var ErrSessionRevoked = errors.New("node: 接入身份已失效")

// renewEarliestRemaining：active 证书剩余有效期超过它时不受理续期。pdnd 在剩余不到 10 天时续期
// （pdnd/panel/types.go 的 RenewBefore），这里多给 1 天容忍两边时钟差；改其一要同步改另一边。
const renewEarliestRemaining = 11 * 24 * time.Hour

// renewMinInterval：上一次续期签出的 pending 不到这么久时不再签新的，防节点异常时高频写身份行。
const renewMinInterval = time.Minute

var (
	errRenewTooEarly    = errors.New("node: 证书剩余有效期还长，暂不续期")
	errRenewTooFrequent = errors.New("node: 刚续过期，稍后再试")
)

// identityNow 是身份相关写库用的当前时间（UTC，截到微秒，与 timestamptz 精度一致）。
func (s *Service) identityNow() time.Time { return s.now().UTC().Truncate(time.Microsecond) }

// AuthorizeSession 在每次建流时调：客户端证书（已由 TLS 层按面板 CA 验过链）的序列号必须是这台服务器当前的
// active 身份，或它续期后还没用过的 pending 身份；pending 第一次通过时转为 active、旧的 active 置 superseded。
// 无效（吊销、过期、被新身份取代、服务器已删或已退役）回 ErrSessionRevoked。
func (s *Service) AuthorizeSession(ctx context.Context, serverID uuid.UUID, certSerial *big.Int) error {
	if certSerial == nil || certSerial.Sign() <= 0 {
		return ErrSessionRevoked
	}
	serial := certSerial.Text(16)
	ident, err := s.repo.SessionIdentity(ctx, serverID, serial, s.identityNow(), false)
	if err != nil {
		return err
	}
	if ident == nil {
		return ErrSessionRevoked
	}
	if ident.Status == identityActive {
		return nil
	}
	return s.activatePendingIdentity(ctx, serverID, serial)
}

// activatePendingIdentity 把节点第一次用上的 pending 身份转正。锁服务器行后重查：并发的两次建流只有一次真正转正，
// 另一次看到的已是 active，直接放行。
//
// 不发 Tombstone：被取代的旧身份就是这个节点自己续期前的证书，它的旧流由会话层按「一台服务器一条流」换掉；
// 给旧流发 tombstone 反而会让节点以为身份被吊销而停止重连。
func (s *Service) activatePendingIdentity(ctx context.Context, serverID uuid.UUID, serial string) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		st, err := s.repo.LockServerForIdentity(ctx, serverID)
		if err != nil {
			return err
		}
		if st == nil || st.Deleted || st.Status == "retired" {
			return ErrSessionRevoked
		}
		now := s.identityNow()
		ident, err := s.repo.SessionIdentity(ctx, serverID, serial, now, true)
		if err != nil {
			return err
		}
		if ident == nil {
			return ErrSessionRevoked
		}
		if ident.Status == identityActive {
			return nil
		}
		oldID, superseded, err := s.repo.SupersedeIdentity(ctx, serverID, identityActive, now)
		if err != nil {
			return err
		}
		ok, err := s.repo.ActivatePendingIdentity(ctx, ident.ID, now)
		if err != nil {
			return err
		}
		if !ok {
			return ErrSessionRevoked
		}
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
		data := map[string]any{"identity_id": ident.ID.String()}
		if superseded {
			data["superseded_identity_id"] = oldID.String()
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action: "server.identity.activate", ResourceType: "server", ResourceID: &serverID, Data: data,
		}); err != nil {
			return err
		}
		plog.From(ctx).Info("续期证书已生效", zap.String("server_id", serverID.String()), zap.String("identity_id", ident.ID.String()))
		return nil
	})
}

// RenewCertificate 用会话里的 CSR 给这台服务器签一张新证书（新私钥由节点生成），登记为 pending；旧的 active
// 保持有效，直到节点用新证书建流。之前续期留下、还没用过的 pending 先置 superseded（至多一份 pending）。
// 续期只认建这条流时出示的证书仍是 active：它被取代或吊销后，拿着旧流不能再换出新证书。
// 返回 PEM 证书链（叶子在前）与 CA bundle。续期不断当前流。
func (s *Service) RenewCertificate(ctx context.Context, serverID uuid.UUID, certSerial *big.Int, csrDER []byte) (chainPEM, caPEM []byte, err error) {
	if certSerial == nil || certSerial.Sign() <= 0 {
		return nil, nil, ErrSessionRevoked
	}
	ca := s.enroll.ca
	if ca == nil {
		return nil, nil, errCANotLoaded
	}
	pub, _, err := parseNodeCSR(csrDER)
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
		if st == nil || st.Deleted || st.Status == "retired" {
			return ErrSessionRevoked
		}
		active, err := s.repo.ActiveIdentity(ctx, serverID)
		if err != nil {
			return err
		}
		if active == nil || !active.ExpiresAt.After(now) {
			return ErrSessionRevoked
		}
		if active.CertSerial != certSerial.Text(16) {
			return ErrSessionRevoked
		}
		if active.ExpiresAt.Sub(now) > renewEarliestRemaining {
			return errRenewTooEarly
		}
		pendingAt, err := s.repo.PendingIdentityIssuedAt(ctx, serverID)
		if err != nil {
			return err
		}
		if pendingAt != nil && now.Sub(*pendingAt) < renewMinInterval {
			return errRenewTooFrequent
		}
		prevID, hadPending, err := s.repo.SupersedeIdentity(ctx, serverID, identityPending, now)
		if err != nil {
			return err
		}
		row, err := s.issueIdentity(ctx, ca, serverID, pub, identityPending, now, nil, nil)
		if err != nil {
			return err
		}
		leafPEM = []byte(row.CertPEM)
		data := map[string]any{
			"identity_id":        row.ID.String(),
			"seq":                row.Seq,
			"active_identity_id": active.ID.String(),
		}
		if hadPending {
			data["superseded_pending_identity_id"] = prevID.String()
		}
		return s.audit.Write(ctx, auditsvc.Event{
			Action: "server.identity.renew", ResourceType: "server", ResourceID: &serverID, Data: data,
		})
	})
	if err != nil {
		return nil, nil, err
	}
	return certChainPEM(ca, leafPEM), ca.CertPEM(), nil
}

// issueIdentity 签证书并写入一份身份（调用方已锁住服务器行、已把会冲突的旧身份置为 superseded）。
// status：接入签出的直接 active，续期签出的 pending。tokenID、requestID 只有接入时有。
func (s *Service) issueIdentity(ctx context.Context, ca *CA, serverID uuid.UUID, pub *ecdsa.PublicKey, status string,
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
		Source:          source,
		EnrollTokenID:   tokenID,
		EnrollRequestID: requestID,
		Status:          status,
		IssuedAt:        now,
		ExpiresAt:       iss.Cert.NotAfter.UTC(),
	}
	if err := s.repo.InsertIdentity(ctx, row); err != nil {
		return nil, err
	}
	s.serverChanged(ctx, realtime.OpUpdate, serverID)
	return row, nil
}
