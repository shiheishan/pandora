package service

// 节点首次接入（EnrollmentService.Enroll）：一次性令牌 + CSR 换 30 天客户端证书（docs/architecture.md 第 11.1 节）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"

	"github.com/google/uuid"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

var (
	// ErrEnrollDenied 是接入被拒：令牌不存在、已用、已过期、服务器不可用、重试的 CSR 对不上。对外一律不区分。
	ErrEnrollDenied = errors.New("node: 接入被拒绝")
	// ErrEnrollInvalid 是请求本身不合法：CSR 不合法、request_id 格式不对、令牌为空或过长。
	ErrEnrollInvalid = errors.New("node: 接入请求不合法")
)

var enrollRequestIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,64}$`)

const enrollTokenMaxLen = 256

// EnrollHostInfo 与 Hello 自报的是同一组主机信息（proto HostInfo）。
type EnrollHostInfo = HostInfo

// EnrollInput 是一次接入请求。
type EnrollInput struct {
	Token        string
	CSRDER       []byte
	RequestID    string
	Host         EnrollHostInfo
	AgentVersion string
	// RemoteIP 是 gRPC 对端 IP（TCP 源地址，不读任何头）：进审计，并用来给地区为空的服务器回填地区。
	RemoteIP string
}

// EnrollResult 是接入结果：服务器 id、客户端证书链（PEM，叶子在前）、CA（PEM）。
type EnrollResult struct {
	ServerID uuid.UUID
	ChainPEM []byte
	CAPEM    []byte
}

// Enroll 用一次性令牌与 CSR 换客户端证书。同一事务里：令牌置已用、这台服务器原有的有效身份置 superseded、
// 写新身份、写主机信息、审计；提交后让会话层断掉旧身份的流。令牌已被同一 request_id 用过（节点崩溃后重试）
// 且 CSR 里是同一把公钥时，返回同一张证书（比公钥不比 CSR 字节：ECDSA 签名带随机数，重签的 CSR 每次都不同）。
func (s *Service) Enroll(ctx context.Context, in EnrollInput) (EnrollResult, error) {
	if in.Token == "" || len(in.Token) > enrollTokenMaxLen || !enrollRequestIDPattern.MatchString(in.RequestID) {
		return EnrollResult{}, ErrEnrollInvalid
	}
	pub, spki, err := parseNodeCSR(in.CSRDER)
	if err != nil {
		return EnrollResult{}, fmt.Errorf("%w: %v", ErrEnrollInvalid, err)
	}
	ca := s.enroll.ca
	if ca == nil {
		return EnrollResult{}, errCANotLoaded
	}
	sum := sha256.Sum256([]byte(in.Token))
	tokenHash := hex.EncodeToString(sum[:])
	now := s.identityNow()

	var res EnrollResult
	var deny string // 只进日志的内部原因
	// 归属地查询要读磁盘、拿解析器的全局锁，放在事务外算好，别在持有令牌行、服务器行锁时做
	var region string
	if in.RemoteIP != "" && s.geo != nil {
		region = regionText(s.geo.Lookup(in.RemoteIP))
	}
	err = s.tx.Do(ctx, func(ctx context.Context) error {
		// 锁序统一为「服务器 → 令牌」（与吊销、退役、删除一致，免得互相死锁）：
		// 先不加锁查出令牌属于哪台服务器，锁服务器行，再锁令牌行重读。令牌的 server_id 有触发器保证不变。
		peekServerID, err := s.repo.BootstrapTokenServerByHash(ctx, tokenHash)
		if err != nil {
			return err
		}
		if peekServerID == nil {
			deny = "token_unknown"
			return ErrEnrollDenied
		}
		st, err := s.repo.LockServerForIdentity(ctx, *peekServerID)
		if err != nil {
			return err
		}
		tok, err := s.repo.BootstrapTokenByHashForUpdate(ctx, tokenHash)
		if err != nil {
			return err
		}
		if tok == nil || tok.ServerID != *peekServerID {
			deny = "token_unknown"
			return ErrEnrollDenied
		}
		if tok.UsedAt != nil {
			if tok.UsedRequestID == nil || *tok.UsedRequestID != in.RequestID {
				deny = "token_used"
				return ErrEnrollDenied
			}
			// 崩溃重试：同一令牌、同一 request_id、同一把公钥，返回同一张证书，不写库；换了公钥一律拒绝
			ident, err := s.repo.IdentityByEnrollToken(ctx, tok.ID)
			if err != nil {
				return err
			}
			if ident == nil || ident.Status != identityActive || ident.SPKISHA256 != spki || !ident.ExpiresAt.After(now) {
				deny = "retry_mismatch"
				return ErrEnrollDenied
			}
			res = EnrollResult{ServerID: ident.ServerID, ChainPEM: certChainPEM(ca, []byte(ident.CertPEM)), CAPEM: ca.CertPEM()}
			return nil
		}
		if !tok.ExpiresAt.After(now) {
			deny = "token_expired"
			return ErrEnrollDenied
		}
		serverID := tok.ServerID
		if st == nil || st.Deleted || st.Status == "retired" {
			deny = "server_unavailable"
			return ErrEnrollDenied
		}
		marked, err := s.repo.MarkBootstrapTokenUsed(ctx, tok.ID, in.RequestID, now)
		if err != nil {
			return err
		}
		if !marked {
			deny = "token_used"
			return ErrEnrollDenied
		}
		// 重新接入：旧的有效身份与没用过的待生效身份都作废
		oldID, superseded, err := s.repo.SupersedeIdentity(ctx, serverID, identityActive, now)
		if err != nil {
			return err
		}
		pendingID, hadPending, err := s.repo.SupersedeIdentity(ctx, serverID, identityPending, now)
		if err != nil {
			return err
		}
		tokenID, requestID := tok.ID, in.RequestID
		row, err := s.issueIdentity(ctx, ca, serverID, pub, identityActive, now, &tokenID, &requestID)
		if err != nil {
			return err
		}
		host := enrollHostInfo(in.Host, in.AgentVersion)
		if err := s.repo.UpdateServerHostInfo(ctx, serverID, host); err != nil {
			return err
		}
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
		if region != "" {
			if err := s.repo.FillServerRegionIfEmpty(ctx, serverID, region); err != nil {
				return err
			}
		}
		agent := ""
		if host.AgentVersion != nil {
			agent = *host.AgentVersion
		}
		data := map[string]any{
			"identity_id":   row.ID.String(),
			"seq":           row.Seq,
			"request_id":    in.RequestID,
			"agent_version": agent,
		}
		if in.RemoteIP != "" {
			data["remote_ip"] = in.RemoteIP
		}
		if superseded {
			data["superseded_identity_id"] = oldID.String()
		}
		if hadPending {
			data["superseded_pending_identity_id"] = pendingID.String()
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action: "server.enrollment.commit", ResourceType: "server", ResourceID: &serverID, Data: data,
		}); err != nil {
			return err
		}
		if superseded {
			// 断掉旧身份的流；新节点要等 Enroll 返回才拿到证书，不会被误断
			tx.AfterCommit(ctx, "node:tombstone:"+serverID.String(), func(context.Context) {
				s.tombstone(serverID, TombstoneIdentitySuperseded)
			})
		}
		res = EnrollResult{ServerID: serverID, ChainPEM: certChainPEM(ca, []byte(row.CertPEM)), CAPEM: ca.CertPEM()}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrEnrollDenied) {
			plog.From(ctx).Warn("节点接入被拒", zap.String("reason", deny), zap.String("enroll_request_id", in.RequestID))
		}
		return EnrollResult{}, err
	}
	return res, nil
}

// enrollHostInfo 把自报主机信息整理成库里的列（与 Hello 走同一套 agentInfo 规整）。
func enrollHostInfo(h EnrollHostInfo, agentVersion string) repo.ServerHostInfo {
	a := agentInfo(Hello{AgentVersion: agentVersion, Host: h})
	return repo.ServerHostInfo{
		Hostname:     a.Hostname,
		OSName:       a.OSName,
		Architecture: a.Architecture,
		AgentVersion: a.AgentVersion,
		CPUCores:     a.CPUCores,
		MemoryMB:     a.MemoryMB,
		DiskGB:       a.DiskGB,
	}
}
