package service

import (
	"context"
	"errors"
	"slices"
	"time"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	"github.com/google/uuid"
)

const (
	orderLease  = IssueTimeout + time.Minute
	issueBudget = 13 * time.Minute
	// maxRunningIssues：同时在跑的签发单上限（旧实现是串行签）
	maxRunningIssues = 2
	// interruptedBackoff：订单因进程退出、任务丢失而中断时的重排间隔，不计连续失败
	interruptedBackoff = 15 * time.Minute
	dueBatch    = 50

	// pauseAfterFailures 连续失败几次后暂停自动重试。
	pauseAfterFailures = 3
)

var errLeaseLost = errors.New("certificate order lease lost")

// orderWork 是处理一张订单要读的全部东西。
type orderWork struct {
	certName           string
	identifiers        []string
	keyType            string
	status             string
	cred               repo.CredentialRow
	secret             map[string]string
	currentIdentifiers []string
	currentCA          *string
	currentAccountID   *uuid.UUID
	notAfter           *time.Time
	cfg                acmeConfig
	recent             []recentIssuance
	now                time.Time
}

// loadOrderWork 在一个事务里取齐处理订单要读的东西：证书与当前版本、凭据明文、ACME 设置、近 7 天签发流水。
func (s *Service) loadOrderWork(ctx context.Context, c repo.ClaimedOrder) (*orderWork, error) {
	w := &orderWork{}
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		row, ok, err := s.store.OrderWork(ctx, c.CertificateID)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		w.certName = row.Name
		w.keyType = row.KeyType
		w.status = row.Status
		w.currentCA = row.CurrentCA
		w.currentAccountID = row.CurrentAccountID
		w.notAfter = row.NotAfter
		ids, err := decodeIDs(row.Identifiers)
		if err != nil {
			return err
		}
		w.identifiers = ids
		if row.CurrentIdentifiers != nil {
			cur, err := decodeIDs(*row.CurrentIdentifiers)
			if err != nil {
				return err
			}
			w.currentIdentifiers = cur
		}
		cred, secret, err := s.loadCredentialSecret(ctx, row.DNSCredentialID)
		if err != nil {
			return err
		}
		w.cred = cred
		w.secret = secret
		w.cfg, err = s.loadACMEConfig(ctx, true)
		if err != nil {
			return err
		}
		w.recent, w.now, err = s.loadRecentIssuances(ctx)
		return err
	})
	return w, err
}

// failureOutcome 是一次没签出来的结论。
type failureOutcome struct {
	code   string
	detail string
	ca     string
	// countsAsFailure：CA 那边真失败了、签发超时或中断，计入连续失败、按 1h→24h 退避，3 次暂停
	countsAsFailure bool
	// retryIn：不计失败时的退避（CA 限流读 Retry-After、本地限额等到最早那张满 7 天、提供方不可达 15 分钟）
	retryIn time.Duration
	// blockCredential：提供方明确拒绝了凭据，凭据标 error，用它的证书都停在 blocked_credential
	blockCredential bool
	// cancel：订单作废（证书已暂停或凭据失效），不算失败
	cancel bool
	// credRowVersion 是预检时读到的凭据版本：只在凭据没被改过时才标 error、拦下证书，
	// 免得管理员刚换的新令牌被旧令牌的结论打回去
	credRowVersion int64
	// credID 是预检时用的凭据：签发期间证书可能被改到别的凭据，不能用落库时读到的证书值
	credID uuid.UUID
	// work 是这一单用的标识与密钥类型；签发期间被改过就补排一张（与成功路径一致）
	work *orderWork
}

// finishFailure 记一次失败：订单结束、证书的退避与失败计数、需要时暂停或拦下凭据，写审计。
// 订单已不是这次认领的（状态变了或 attempt 变了）回 errLeaseLost，什么也不写。
func (s *Service) finishFailure(ctx context.Context, c repo.ClaimedOrder, f failureOutcome) error {
	return s.tx.Do(ctx, func(ctx context.Context) error {
		mine, err := s.store.LockOwnedOrder(ctx, c.ID, c.Attempt)
		if err != nil {
			return err
		}
		if !mine {
			return errLeaseLost
		}
		state := OrderFailed
		if f.cancel {
			state = OrderCancelled
		}
		detail := truncate(f.detail, 2000)
		if err := s.store.FinishOrderFailed(ctx, c.ID, state, f.code, detail, f.ca); err != nil {
			return err
		}
		if f.cancel {
			return nil
		}
		cert, ok, err := s.store.LockCertificate(ctx, c.CertificateID)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		failures := cert.ConsecutiveFailures
		retry := f.retryIn
		paused := false
		if f.countsAsFailure {
			failures++
			retry = failureBackoff(failures)
			paused = failures >= pauseAfterFailures && (cert.Status == StatusPending || cert.Status == StatusActive)
		}
		if err := s.store.ApplyFailure(ctx, c.CertificateID, repo.FailureUpdate{
			Failures: failures, RetryIn: retry, Code: f.code, Detail: detail, Pause: paused,
		}); err != nil {
			return err
		}
		credentialBlocked := false
		if f.blockCredential {
			m := truncate(f.detail, 500)
			wrote, err := s.store.RecordCredentialVerdict(ctx, f.credID, f.credRowVersion, repo.CredentialVerdict{
				Status: VerifyError, VerifyError: &m,
			})
			if err != nil {
				return err
			}
			if wrote {
				credentialBlocked = true
				if err := s.store.BlockCertificatesByCredential(ctx, f.credID); err != nil {
					return err
				}
			}
		}
		curIDs, err := decodeIDs(cert.Identifiers)
		if err != nil {
			return err
		}
		if f.work != nil && !paused && (cert.Status == StatusPending || cert.Status == StatusActive) &&
			(!slices.Equal(curIDs, f.work.identifiers) || cert.KeyType != f.work.keyType) {
			if _, _, err := s.queueOrder(ctx, c.CertificateID, ReasonManual); err != nil {
				return err
			}
		}
		cid := c.CertificateID
		if err := s.writeAudit(ctx, "certificate.issue_failed", "certificate", &cid, auditsvc.OutcomeFailure, map[string]any{
			"order_id": c.ID, "code": f.code, "consecutive_failures": failures,
			"retry_in_seconds": int64(retry.Seconds()), "credential_blocked": credentialBlocked,
		}); err != nil {
			return err
		}
		if !paused {
			return nil
		}
		return s.writeAudit(ctx, "certificate.paused", "certificate", &cid, "", map[string]any{
			"reason": "failures", "consecutive_failures": failures,
		})
	})
}

// finishSuccess 落库一张签出的证书：新版本（私钥密文）、证书的当前版本与续期时间、订单完成、审计，
// 同一个事务。签发期间证书的标识或密钥类型被改过，就接着再排一张订单。
func (s *Service) finishSuccess(ctx context.Context, c repo.ClaimedOrder, w *orderWork, target caTarget,
	accountID uuid.UUID, replaces string, iss *issued) (int, error) {
	var version int
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		mine, err := s.store.LockOwnedOrder(ctx, c.ID, c.Attempt)
		if err != nil {
			return err
		}
		if !mine {
			return errLeaseLost
		}
		cert, ok, err := s.store.LockCertificate(ctx, c.CertificateID)
		if err != nil {
			return err
		}
		if !ok {
			return notFound()
		}
		curIDs, err := decodeIDs(cert.Identifiers)
		if err != nil {
			return err
		}
		curKey := cert.KeyType
		version, err = s.store.NextVersion(ctx, c.CertificateID)
		if err != nil {
			return err
		}
		renewal := w.currentIdentifiers != nil && slices.Equal(w.currentIdentifiers, w.identifiers)
		sealed, err := s.sealer.Seal(iss.keyDER, versionKeyAAD(c.CertificateID, version))
		if err != nil {
			return err
		}
		versionID := uuid.Must(uuid.NewV7())
		var ariCertID, certURL *string
		if iss.ariCertID != "" {
			ariCertID = &iss.ariCertID
		}
		if u := truncate(iss.certURL, 500); u != "" {
			certURL = &u
		}
		if err := s.store.InsertVersion(ctx, &model.CertificateVersion{
			ID: versionID, CertificateID: c.CertificateID, Version: version, ACMEAccountID: accountID,
			CA: target.CA, Identifiers: encodeIDs(w.identifiers), IsRenewal: renewal, Serial: serialHex(iss.leaf),
			ChainPEM: string(iss.chainPEM), PrivateKeySealed: sealed, PublicKeySHA256: iss.pubSHA256,
			ChainSHA256: iss.chainSHA256, NotBefore: iss.leaf.NotBefore, NotAfter: iss.leaf.NotAfter,
			ARICertID: ariCertID, CertURL: certURL, OrderID: &c.ID,
		}); err != nil {
			return err
		}
		if err := s.store.ApplyIssued(ctx, c.CertificateID, repo.SuccessUpdate{
			VersionID: versionID, NotBefore: iss.leaf.NotBefore, NotAfter: iss.leaf.NotAfter,
			RenewAfter: defaultRenewAfter(iss.leaf.NotBefore, iss.leaf.NotAfter),
		}); err != nil {
			return err
		}
		if err := s.store.FinishOrderSucceeded(ctx, c.ID, versionID, target.CA, replaces); err != nil {
			return err
		}
		if !slices.Equal(curIDs, w.identifiers) || curKey != w.keyType {
			if _, _, err := s.queueOrder(ctx, c.CertificateID, ReasonManual); err != nil {
				return err
			}
		}
		cid := c.CertificateID
		return s.writeAudit(ctx, "certificate.issued", "certificate", &cid, "", map[string]any{
			"version": version, "ca": target.CA, "serial": serialHex(iss.leaf), "identifiers": w.identifiers,
			"not_after": iss.leaf.NotAfter, "order_id": c.ID, "reason": c.Reason, "replaces": replaces != "",
		})
	})
	return version, err
}
