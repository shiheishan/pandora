package nodefabric

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/bindingcontract"
	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 服务器级绑定（合约 docs/server-binding-contract.md）的服务器身份：验签、吊销、绑定状态。
//
// 和按节点身份（node_identities）并存：服务器身份只认 /v1/servers/* 的签名请求，
// 节点身份只认 /v1/nodes/* 与 UniProxy，两边互不放行。

// 服务器身份吊销理由，取合约清单墓碑 unbound_reason 的取值（§4.2），P2 原样下发给 pdnd。
const (
	ServerRevokedDeleted  = "server_deleted" // 服务器被删除或退役
	ServerRevokedUnbound  = "server_unbound" // 管理员在面板上解除绑定
	ServerRevokedIdentity = "identity_revoked"
	ServerRevokedMachine  = "machine_unbound" // 机器侧主动解绑（S7，P2）
)

// 服务器请求验签的失败分类。对外一律同一个 401「服务器身份校验失败」，不区分原因；
// 后端（库、Valkey）不可用单独归类，网关回 503，免得 pdnd 把一次库抖动当成身份被吊销。
var (
	ErrServerIdentityInvalid   = errors.New("server identity is missing or revoked")
	ErrServerSignatureMismatch = errors.New("server request signature mismatch")
	ErrServerAuthUnavailable   = errors.New("server authentication backend unavailable")
)

// ServerIdentity 是一次验签通过的服务器身份。
type ServerIdentity struct {
	ServerID  string
	Serial    int64
	PublicKey ed25519.PublicKey
}

// activeServerIdentitySQL 是「服务器有一份有效身份」的唯一口径：身份 active，
// 服务器没有删除、没有退役。参数：$1 租户、$2 服务器、$3 serial。
const activeServerIdentitySQL = `
	SELECT i.public_key
	  FROM server_identities i
	  JOIN servers s ON s.tenant_id = i.tenant_id AND s.id = i.server_id
	 WHERE i.tenant_id = $1 AND i.server_id = $2::uuid AND i.serial = $3 AND i.status = 'active'
	   AND s.deleted_at IS NULL AND s.status <> 'retired'`

// VerifyServerRequest 按合约 §2.4 第 4 步验服务器请求签名：按（租户, 服务器, serial）取 active
// 的公钥验原像。吊销、不存在、serial 不符一律 ErrServerIdentityInvalid；原像由调用方用
// bindingcontract.ServerRequestPreimage 按收到的请求重建。
func (s *Service) VerifyServerRequest(ctx context.Context, tenantID, serverID string, serial int64,
	preimage, signature []byte) (*ServerIdentity, error) {
	if _, err := canonicalUUIDField("server_id", serverID); err != nil || serial <= 0 || serial > 1<<31-1 {
		return nil, ErrServerIdentityInvalid
	}
	var pub []byte
	err := s.pool.QueryRowScoped(ctx, db.Scope{TenantID: tenantID}, activeServerIdentitySQL,
		[]any{tenantID, serverID, serial}, &pub)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrServerIdentityInvalid
	}
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrServerAuthUnavailable, err)
	}
	if len(pub) != ed25519.PublicKeySize {
		return nil, ErrServerIdentityInvalid
	}
	if !crypto.Verify(ed25519.PublicKey(pub), preimage, signature) {
		return nil, ErrServerSignatureMismatch
	}
	return &ServerIdentity{ServerID: serverID, Serial: serial, PublicKey: ed25519.PublicKey(pub)}, nil
}

// revokeServerBindingTx 在调用方的事务里收回一台服务器的全部绑定凭据：吊销 active 身份、
// 中止进行中的接入、作废还没用掉的绑定令牌。返回被吊销身份的 serial（没有为 0）。
// 调用方先锁好服务器行。
func revokeServerBindingTx(ctx context.Context, tx pgx.Tx, tenantID, serverID, reason string) (int, int64, int64, error) {
	var revoked int
	err := tx.QueryRow(ctx, `UPDATE server_identities SET status='revoked', revoked_reason=$3
		WHERE tenant_id=$1 AND server_id=$2::uuid AND status='active' RETURNING serial`,
		tenantID, serverID, reason).Scan(&revoked)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, 0, err
	}
	aborted, err := tx.Exec(ctx, `UPDATE server_enrollments SET state='aborted', abort_reason=$3
		WHERE tenant_id=$1 AND server_id=$2::uuid AND state='pending'`, tenantID, serverID, "binding revoked: "+reason)
	if err != nil {
		return 0, 0, 0, err
	}
	voided, err := tx.Exec(ctx, `UPDATE bootstrap_tokens SET consumed_at=now()
		WHERE tenant_id=$1 AND server_id=$2::uuid AND kind='server' AND consumed_at IS NULL`, tenantID, serverID)
	if err != nil {
		return 0, 0, 0, err
	}
	return revoked, aborted.RowsAffected(), voided.RowsAffected(), nil
}

//------------------------------------------------------------------------------
// 后台：绑定状态与解除绑定
//------------------------------------------------------------------------------

// 绑定状态（由身份与接入现算，不存列，所以不会和事实漂移）。
const (
	ServerBindingUnbound = "unbound" // 从未绑定
	ServerBindingBinding = "binding" // 有进行中的接入，还没有有效身份
	ServerBindingBound   = "bound"   // 有一份有效身份
	ServerBindingRevoked = "revoked" // 绑定过，身份已被吊销
)

// ServerBindingStatus 是后台「服务器绑定」卡片读的状态。
type ServerBindingStatus struct {
	ServerID string `json:"server_id"`
	State    string `json:"state"`
	// PanelKey 是本面板配置签名公钥的 --panel-key 写法，与绑定命令里的一致。
	PanelKey             string                   `json:"panel_key"`
	Identity             *ServerIdentityInfo      `json:"identity"`
	PendingEnrollment    *ServerPendingEnrollment `json:"pending_enrollment"`
	BindingTokensPending int                      `json:"binding_tokens_pending"`
	LastHeartbeatAt      *time.Time               `json:"last_heartbeat_at"`
}

// ServerIdentityInfo 只给元数据：公钥本身不回给后台，给 key id 与完整指纹足够核对。
type ServerIdentityInfo struct {
	Serial            int             `json:"serial"`
	Status            string          `json:"status"`
	KeyID             string          `json:"key_id"`
	FingerprintSHA256 string          `json:"fingerprint_sha256"`
	EncKeyID          string          `json:"enc_key_id"`
	AgentVersion      string          `json:"agent_version"`
	Features          []string        `json:"features"`
	Capabilities      json.RawMessage `json:"capabilities"`
	IssuedAt          time.Time       `json:"issued_at"`
	RevokedAt         *time.Time      `json:"revoked_at,omitempty"`
	RevokedReason     *string         `json:"revoked_reason,omitempty"`
}

// ServerPendingEnrollment 是进行中、还没到期的那次接入。
type ServerPendingEnrollment struct {
	EnrollmentID string    `json:"enrollment_id"`
	Serial       int       `json:"serial"`
	AgentVersion string    `json:"agent_version"`
	Hostname     *string   `json:"hostname"`
	ExpiresAt    time.Time `json:"expires_at"`
}

// panelKeyPin 是本面板配置签名公钥的 --panel-key 钉住值（合约 §11.2）。
func (s *Service) panelKeyPin() (string, error) {
	return bindingcontract.PanelKeyFingerprint(s.signer.PublicKey())
}

// ServerBinding 读一台服务器的绑定状态。
func (s *Service) ServerBinding(ctx context.Context, tenantID, serverID string) (*ServerBindingStatus, error) {
	if err := validateAdminUUID("server_id", serverID, true); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	pin, err := s.panelKeyPin()
	if err != nil {
		return nil, httpx.Internal(err)
	}
	out := ServerBindingStatus{ServerID: serverID, PanelKey: pin}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return readServerBindingTx(ctx, tx, tenantID, serverID, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func readServerBindingTx(ctx context.Context, tx pgx.Tx, tenantID, serverID string, out *ServerBindingStatus) error {
	if err := tx.QueryRow(ctx, `SELECT last_heartbeat_at FROM servers
		WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL`, tenantID, serverID).
		Scan(&out.LastHeartbeatAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFoundOrForbidden()
		}
		return err
	}
	// 有效身份优先；没有就给最近一份（被吊销的），让管理员看见「绑过、为什么没了」
	var id ServerIdentityInfo
	var pub, fp, encPub, caps []byte
	err := tx.QueryRow(ctx, `
		SELECT serial, status, public_key, fingerprint, enc_public_key, agent_version, features,
		       capabilities, issued_at, revoked_at, revoked_reason
		  FROM server_identities
		 WHERE tenant_id=$1 AND server_id=$2::uuid
		 ORDER BY (status='active') DESC, serial DESC
		 LIMIT 1`, tenantID, serverID).Scan(&id.Serial, &id.Status, &pub, &fp, &encPub,
		&id.AgentVersion, &id.Features, &caps, &id.IssuedAt, &id.RevokedAt, &id.RevokedReason)
	switch {
	case err == nil:
		id.KeyID = bindingcontract.KeyID(pub)
		id.FingerprintSHA256 = hex.EncodeToString(fp)
		id.EncKeyID = bindingcontract.KeyID(encPub)
		if id.Features == nil {
			id.Features = []string{}
		}
		if len(caps) != 0 {
			id.Capabilities = json.RawMessage(caps)
		}
		id.IssuedAt = id.IssuedAt.UTC()
		out.Identity = &id
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	var pending ServerPendingEnrollment
	err = tx.QueryRow(ctx, `
		SELECT id::text, candidate_serial, agent_version, hostname, expires_at
		  FROM server_enrollments
		 WHERE tenant_id=$1 AND server_id=$2::uuid AND state='pending' AND expires_at > clock_timestamp()`,
		tenantID, serverID).Scan(&pending.EnrollmentID, &pending.Serial, &pending.AgentVersion,
		&pending.Hostname, &pending.ExpiresAt)
	switch {
	case err == nil:
		pending.ExpiresAt = pending.ExpiresAt.UTC()
		out.PendingEnrollment = &pending
	case !errors.Is(err, pgx.ErrNoRows):
		return err
	}
	if err := tx.QueryRow(ctx, `SELECT count(*)::int FROM bootstrap_tokens
		WHERE tenant_id=$1 AND server_id=$2::uuid AND kind='server'
		  AND consumed_at IS NULL AND used_count < max_uses AND expires_at > now()`,
		tenantID, serverID).Scan(&out.BindingTokensPending); err != nil {
		return err
	}
	switch {
	case out.Identity != nil && out.Identity.Status == "active":
		out.State = ServerBindingBound
	case out.PendingEnrollment != nil:
		out.State = ServerBindingBinding
	case out.Identity != nil:
		out.State = ServerBindingRevoked
	default:
		out.State = ServerBindingUnbound
	}
	return nil
}

// UnbindServer 是后台「解除绑定」：吊销服务器身份、中止进行中的接入、作废未用的绑定令牌。
// 吊销后这台机器的每个服务器签名请求都回 401；P2 起清单改回签名的 server_unbound 墓碑，
// pdnd 据此停服清理。没有可收回的凭据时照样成功（重复点击不报错）。
func (s *Service) UnbindServer(ctx context.Context, tenantID, actorID, serverID string) (*ServerBindingStatus, error) {
	if err := validateAdminUUID("server_id", serverID, true); err != nil {
		return nil, httpx.NotFoundOrForbidden()
	}
	pin, err := s.panelKeyPin()
	if err != nil {
		return nil, httpx.Internal(err)
	}
	out := ServerBindingStatus{ServerID: serverID, PanelKey: pin}
	err = s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: actorID}, func(tx pgx.Tx) error {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT true FROM servers
			WHERE tenant_id=$1 AND id=$2::uuid AND deleted_at IS NULL FOR UPDATE`, tenantID, serverID).
			Scan(&exists); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return httpx.NotFoundOrForbidden()
			}
			return err
		}
		revoked, aborted, voided, err := revokeServerBindingTx(ctx, tx, tenantID, serverID, ServerRevokedUnbound)
		if err != nil {
			return err
		}
		if err := audit.Write(ctx, tx, tenantID, audit.Entry{
			ActorKind: "admin", ActorID: nullStr(actorID), Action: "server.binding.unbind",
			ResourceType: "server", ResourceID: &serverID, APIDomain: "admin",
			RequestID: httpx.RequestIDFrom(ctx),
			AfterDigest: map[string]any{"revoked_serial": revoked, "aborted_enrollments": aborted,
				"voided_tokens": voided, "reason": ServerRevokedUnbound},
		}); err != nil {
			return err
		}
		return readServerBindingTx(ctx, tx, tenantID, serverID, &out)
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// encodeKey 是线上公钥的写法：32 字节原始公钥的标准 base64。
func encodeKey(raw []byte) string { return base64.StdEncoding.EncodeToString(raw) }
