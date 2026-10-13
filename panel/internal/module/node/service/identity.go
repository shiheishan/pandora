package service

import (
	"context"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

// InstallTarget 是安装命令里的面板地址：BaseURL 是 public_base_url（下载安装脚本），Host、Port 是节点 gRPC 的对外地址。
type InstallTarget struct {
	BaseURL string
	Host    string
	Port    int
}

// enrollState 是接入与身份的启动装配（UseEnrollment 注入）。
type enrollState struct {
	ca      *CA
	install InstallTarget
}

// server_identities.status 的取值。
const (
	identityPending = "pending"
	identityActive  = "active"
)

// 吊销与取代时给会话层的 Tombstone 原因。
const (
	TombstoneIdentityRevoked    = "identity_revoked"
	TombstoneIdentitySuperseded = "identity_superseded"
	// TombstoneServerRetired 服务器退役后断流
	TombstoneServerRetired = "server_retired"
	// TombstoneServerDeleted 服务器删除后断流
	TombstoneServerDeleted = "server_deleted"
)

const identityRevokeReasonAdmin = "后台吊销"

const (
	identityRevokeReasonRetired = "服务器已退役"
	identityRevokeReasonDeleted = "服务器已删除"
)

var (
	errCANotLoaded    = errs.Newf(errs.Unavailable, "节点 CA 未装配")
	errEnrollNotReady = errs.Newf(errs.Unavailable, "节点通道未配置（grpc.node.public_host），不能签发安装令牌")
)

// UseEnrollment 只在启动装配时调用（不并发安全）。install.Host 为空表示节点通道没配置：签发令牌回 503。
func (s *Service) UseEnrollment(ca *CA, install InstallTarget) {
	s.enroll = enrollState{ca: ca, install: install}
}

// CAFingerprint 返回面板 CA 指纹（sha256:<hex>）；CA 未装配回 errCANotLoaded。
func (s *Service) CAFingerprint(ctx context.Context) (string, error) {
	if s.enroll.ca == nil {
		return "", errCANotLoaded
	}
	return s.enroll.ca.Fingerprint(), nil
}

// HasActiveIdentity 这台服务器有没有未过期的有效身份（上线检查用）。
func (s *Service) HasActiveIdentity(ctx context.Context, serverID uuid.UUID) (bool, error) {
	return s.repo.HasActiveIdentity(ctx, serverID, s.identityNow())
}

// IdentityInfo 是后台展示的一份身份。
type IdentityInfo struct {
	Seq               int32
	Status            string // active、expired（active 但已过期）、superseded、revoked
	SPIFFEID          string
	FingerprintSHA256 string // 客户端证书公钥 SPKI 的 SHA-256 小写 hex（库里 spki_sha256）
	IssuedAt          time.Time
	ExpiresAt         time.Time
	RevokedAt         *time.Time
	RevokedReason     *string
}

// NodeIdentityView 是节点抽屉「身份与令牌」页的数据：节点所在服务器的最新身份与待用令牌数。
type NodeIdentityView struct {
	Identity      *IdentityInfo // nil = 从未接入或节点没挂服务器
	PendingTokens int64
}

// NodeIdentity 返回节点所在服务器的身份。节点不存在或已删除回 errs.New(errs.NotFound)；没挂服务器回空视图。
func (s *Service) NodeIdentity(ctx context.Context, nodeID uuid.UUID) (NodeIdentityView, error) {
	serverID, found, err := s.repo.NodeServerID(ctx, nodeID)
	if err != nil {
		return NodeIdentityView{}, err
	}
	if !found {
		return NodeIdentityView{}, errs.New(errs.NotFound)
	}
	if serverID == nil {
		return NodeIdentityView{}, nil
	}
	now := s.identityNow()
	id, err := s.repo.LatestIdentity(ctx, *serverID)
	if err != nil {
		return NodeIdentityView{}, err
	}
	pending, err := s.repo.CountPendingBootstrapTokens(ctx, *serverID, now)
	if err != nil {
		return NodeIdentityView{}, err
	}
	view := NodeIdentityView{PendingTokens: pending}
	if id != nil {
		view.Identity = identityInfoFromModel(id, now)
	}
	return view, nil
}

func identityInfoFromModel(id *model.ServerIdentity, now time.Time) *IdentityInfo {
	status := id.Status
	if status == identityActive && !id.ExpiresAt.After(now) {
		status = "expired"
	}
	return &IdentityInfo{
		Seq:               id.Seq,
		Status:            status,
		SPIFFEID:          ServerSPIFFEID(id.ServerID),
		FingerprintSHA256: id.SPKISHA256,
		IssuedAt:          id.IssuedAt,
		ExpiresAt:         id.ExpiresAt,
		RevokedAt:         id.RevokedAt,
		RevokedReason:     id.RevokedReason,
	}
}

// RevokeNodeIdentity 吊销节点所在服务器的有效身份；提交后让会话层断掉这台服务器的流（Tombstone）。
func (s *Service) RevokeNodeIdentity(ctx context.Context, nodeID uuid.UUID) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		serverID, found, err := s.repo.NodeServerID(ctx, nodeID)
		if err != nil {
			return err
		}
		if !found {
			return errs.New(errs.NotFound)
		}
		if serverID == nil {
			return errs.Newf(errs.Conflict, "节点还没放到服务器上，没有接入身份")
		}
		sid := *serverID
		st, err := s.repo.LockServerForIdentity(ctx, sid)
		if err != nil {
			return err
		}
		if st == nil {
			return errs.New(errs.NotFound)
		}
		revoked, voided, err := s.revokeServerIdentities(ctx, sid, identityRevokeReasonAdmin, TombstoneIdentityRevoked)
		if err != nil {
			return err
		}
		if len(revoked) == 0 {
			return errs.Newf(errs.Conflict, "这台服务器没有有效的接入身份")
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.identity.revoke",
			ResourceType: "server",
			ResourceID:   &sid,
			Data: map[string]any{
				"node_id":       nodeID.String(),
				"identity_ids":  identityIDStrings(revoked),
				"voided_tokens": voided,
			},
		}); err != nil {
			return err
		}
		return nil
	})
}

// revokeServerIdentities 在调用方事务里吊销这台服务器的有效与待生效身份、作废未用未过期的接入令牌，
// 并登记提交后让会话层断掉这台服务器的流。调用方已锁住服务器行。
func (s *Service) revokeServerIdentities(ctx context.Context, serverID uuid.UUID, reason, tombstoneReason string) (revoked []uuid.UUID, voided int64, err error) {
	now := s.identityNow()
	// 有效身份与续期后还没用过的待生效身份一起吊销，免得待生效的那张之后还能转正
	revoked, err = s.repo.RevokeIdentities(ctx, serverID, reason, now)
	if err != nil {
		return nil, 0, err
	}
	// 事发处置：吊销前签出的未用令牌不能再把这台机器接回来
	voided, err = s.repo.VoidBootstrapTokens(ctx, serverID, now)
	if err != nil {
		return nil, 0, err
	}
	tx.AfterCommit(ctx, "node:tombstone:"+serverID.String(), func(context.Context) {
		s.tombstone(serverID, tombstoneReason)
	})
	return revoked, voided, nil
}

// identityIDStrings 把身份 id 转成字符串（审计 Data 用），永不为 nil。
func identityIDStrings(ids []uuid.UUID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = id.String()
	}
	return out
}

// tombstone 通知会话层（没装配时什么也不做）。
func (s *Service) tombstone(serverID uuid.UUID, reason string) {
	if s.sessions != nil {
		s.sessions.Tombstone(serverID, reason)
	}
}
