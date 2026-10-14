package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	idemsvc "github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// errIdemNotWired 是签安装令牌时带了幂等认领但未装配完成端口。
var errIdemNotWired = errors.New("node: 幂等完成端口未装配，拒绝签发以免令牌明文落进幂等表")

// IdemCompleter 是幂等 service 在业务事务内以「不可重放」完成认领的方法；*idemsvc.Service 直接满足。
type IdemCompleter interface {
	CompleteUnreplayable(ctx context.Context, c *idemsvc.Claim, status int) error
}

var _ IdemCompleter = (*idemsvc.Service)(nil)

// UseIdempotency 注入幂等完成端口；只在启动装配时调用（不并发安全），签安装令牌要用它。
func (s *Service) UseIdempotency(c IdemCompleter) { s.idem = c }

const (
	bootstrapTTLDefault = 20 // 分钟
	bootstrapTTLMax     = 60
	bootstrapTokenBytes = 32
)

// BootstrapTokenIssued 是签发结果：令牌明文只在这里出现一次。
type BootstrapTokenIssued struct {
	Token          string
	ExpiresAt      time.Time
	InstallCommand string
}

// IssueBootstrapToken 给服务器签一张一次性安装令牌（ttlMinutes 为 0 用缺省 20 分钟，1–60），返回令牌与安装命令。
// 服务器不存在回 NotFound；已删除或已退役回 409。带幂等认领（经幂等中间件）时在同一事务里以不可重放完成认领。
func (s *Service) IssueBootstrapToken(ctx context.Context, serverID uuid.UUID, ttlMinutes int) (BootstrapTokenIssued, error) {
	ttl := ttlMinutes
	if ttl == 0 {
		ttl = bootstrapTTLDefault
	}
	if ttl < 1 || ttl > bootstrapTTLMax {
		return BootstrapTokenIssued{}, errs.Invalid(map[string]string{
			"ttl_minutes": "有效期须在 1–60 分钟",
		})
	}
	if s.enroll.ca == nil {
		return BootstrapTokenIssued{}, errCANotLoaded
	}
	if s.enroll.install.Host == "" {
		return BootstrapTokenIssued{}, errEnrollNotReady
	}

	buf := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return BootstrapTokenIssued{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	hash := sha256.Sum256([]byte(token))
	tokenSHA256 := hex.EncodeToString(hash[:])

	var issuedBy *uuid.UUID
	if p, ok := authn.From(ctx); ok && p.UserID != uuid.Nil {
		issuedBy = &p.UserID
	}

	now := s.identityNow()
	exp := now.Add(time.Duration(ttl) * time.Minute)

	var out BootstrapTokenIssued
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		st, err := s.repo.LockServerForIdentity(ctx, serverID)
		if err != nil {
			return err
		}
		if st == nil {
			return errs.New(errs.NotFound)
		}
		if st.Deleted || st.Status == "retired" {
			return errs.Newf(errs.Conflict, "服务器已删除或已退役，不能签发安装令牌")
		}
		t := &model.BootstrapToken{
			ServerID:    serverID,
			TokenSHA256: tokenSHA256,
			ExpiresAt:   exp,
			IssuedBy:    issuedBy,
			CreatedAt:   now,
		}
		if err := s.repo.InsertBootstrapToken(ctx, t); err != nil {
			return err
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "node.bootstrap_token.issue",
			ResourceType: "server",
			ResourceID:   &serverID,
			Data: map[string]any{
				"token_id":    t.ID.String(),
				"ttl_minutes": ttl,
				"expires_at":  exp,
			},
		}); err != nil {
			return err
		}
		out = BootstrapTokenIssued{
			Token:          token,
			ExpiresAt:      exp,
			InstallCommand: buildInstallCommand(s.enroll.install, s.enroll.ca.Fingerprint(), token),
		}
		// 响应里有令牌明文和整条安装命令：幂等认领在业务事务里完成成「不可重放」，响应体不落进幂等表
		// （否则明文令牌会在库里存到幂等记录过期，而 bootstrap_tokens 刻意只存哈希）。重放回 409，管理员重新签一张即可。
		if claim, ok := idemsvc.ClaimFrom(ctx); ok {
			if s.idem == nil {
				return errs.Wrap(errs.Internal, errIdemNotWired)
			}
			return s.idem.CompleteUnreplayable(ctx, claim, http.StatusCreated)
		}
		return nil
	})
	if err != nil {
		return BootstrapTokenIssued{}, err
	}
	return out, nil
}

// buildInstallCommand 拼安装命令（每个参数都用单引号包起来）：
// curl --proto '=https' -fsSL '<BaseURL 去掉末尾 />/pdnd/install.sh' | sudo bash -s -- --panel '<host:port>' --ca-sha256 '<fp>' --token '<token>'
// host:port 用 net.JoinHostPort（IPv6 自动加方括号）。https 面板加 --proto '=https'：以 root 运行的脚本不许被降级到 http；
// 本机开发的回环 http 面板不加。
func buildInstallCommand(t InstallTarget, caFingerprint, token string) string {
	base := strings.TrimSuffix(strings.TrimSpace(t.BaseURL), "/")
	script := base + "/pdnd/install.sh"
	hostPort := net.JoinHostPort(t.Host, strconv.Itoa(t.Port))
	curl := "curl -fsSL "
	if strings.HasPrefix(base, "https://") {
		curl = "curl --proto '=https' -fsSL "
	}
	return curl + shellQuote(script) + " | sudo bash -s -- --panel " + shellQuote(hostPort) +
		" --ca-sha256 " + shellQuote(caFingerprint) + " --token " + shellQuote(token)
}

// shellQuote 用单引号包裹并把内部的 ' 转成 '\”。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
