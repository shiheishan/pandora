package service

import (
	"context"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/aegispanel/aegis/internal/module/certs/repo"
	"github.com/google/uuid"
)

// store 是 service 对 repo 的全部要求；*repo.Repo 实现它。所有方法经 ctx 取连接（有事务就加入）。
// 「找不到」一律以 ok=false 返回，不当错误；唯一约束冲突返回 repo.ErrDuplicate。
type store interface {
	// DBNow 是数据库时钟（排程、退避、限额对账一律用它）。
	DBNow(ctx context.Context) (time.Time, error)

	// --- DNS 凭据 ---

	// ListCredentials 按 lower(name), id 排序列出全部凭据。
	ListCredentials(ctx context.Context) ([]repo.CredentialRow, error)
	// GetCredential 读一个凭据；lock 为真时 FOR UPDATE 锁住凭据行。
	GetCredential(ctx context.Context, id uuid.UUID, lock bool) (repo.CredentialRow, bool, error)
	// CredentialSecret 读凭据的封存密文。
	CredentialSecret(ctx context.Context, id uuid.UUID) ([]byte, bool, error)
	// CredentialExists 报告凭据是否存在。
	CredentialExists(ctx context.Context, id uuid.UUID) (bool, error)
	// InsertCredential 插入凭据（ID 由调用方生成，密文的 AAD 绑定它）；重名回 repo.ErrDuplicate。
	InsertCredential(ctx context.Context, c *model.DNSCredential) error
	// UpdateCredential 改名、改 zone、按需换密钥并 row_version + 1；重名回 repo.ErrDuplicate。
	UpdateCredential(ctx context.Context, id uuid.UUID, u repo.CredentialUpdate) error
	// DeleteCredential 删除凭据。
	DeleteCredential(ctx context.Context, id uuid.UUID) error
	// CertificateNamesByCredential 返回引用该凭据的证书名（按 lower(name) 排序，最多 limit 个）。
	CertificateNamesByCredential(ctx context.Context, id uuid.UUID, limit int) ([]string, error)
	// RecordCredentialVerdict 只在 row_version 仍等于 rowVersion 时写校验结论（ok 时 verified_at = now()）；
	// 返回是否写入。
	RecordCredentialVerdict(ctx context.Context, id uuid.UUID, rowVersion int64, v repo.CredentialVerdict) (bool, error)
	// BlockCertificatesByCredential 把引用该凭据、处于 pending / active 的证书转 blocked_credential（row_version + 1）。
	BlockCertificatesByCredential(ctx context.Context, credentialID uuid.UUID) error
	// ResumeCertificatesByCredential 把引用该凭据、停在 blocked_credential 的证书恢复（没签出过回 pending，
	// 签出过回 active），清 next_attempt_at；返回恢复的张数。
	ResumeCertificatesByCredential(ctx context.Context, credentialID uuid.UUID) (int, error)

	// --- 证书 ---

	// ListCertificates 按 lower(name), id 排序列出全部证书（一条 SQL，带凭据与当前版本、进行中的订单）。
	ListCertificates(ctx context.Context) ([]repo.CertificateRow, error)
	// GetCertificate 读一张证书的读形状。
	GetCertificate(ctx context.Context, id uuid.UUID) (repo.CertificateRow, bool, error)
	// LockCertificate 以 FOR UPDATE 锁住证书行并返回它。
	LockCertificate(ctx context.Context, id uuid.UUID) (model.Certificate, bool, error)
	// InsertCertificate 插入证书；重名回 repo.ErrDuplicate。
	InsertCertificate(ctx context.Context, c *model.Certificate) error
	// UpdateCertificateSpec 改名、标识、凭据、密钥类型并 row_version + 1；重名回 repo.ErrDuplicate。
	UpdateCertificateSpec(ctx context.Context, id uuid.UUID, s repo.CertificateSpec) error
	// SetCertificateStatus 把状态从 from 改成 to（paused 时 paused_reason = reason，否则清空）；resetBackoff 为真时
	// 同时清 consecutive_failures 与 next_attempt_at。row_version + 1。返回是否命中（状态已变返回 false）。
	SetCertificateStatus(ctx context.Context, id uuid.UUID, from, to string, reason *string, resetBackoff bool) (bool, error)
	// DeleteCertificate 删除证书与它的全部订单、版本（先清当前版本，再删订单、版本、证书）。
	DeleteCertificate(ctx context.Context, id uuid.UUID) error
	// ListVersions 按版本号降序列出证书的全部版本。
	ListVersions(ctx context.Context, certificateID uuid.UUID) ([]repo.VersionRow, error)
	// ListOrders 按 created_at DESC, id DESC 列出证书最近 limit 张订单。
	ListOrders(ctx context.Context, certificateID uuid.UUID, limit int) ([]repo.OrderRow, error)

	// --- 订单 ---

	// LockActiveOrders 以 FOR UPDATE 锁住证书进行中（queued / running）的订单并返回其中是否有 running。
	LockActiveOrders(ctx context.Context, certificateID uuid.UUID) (running bool, err error)
	// QueueOrder 给证书排一张订单（replaces 取当前版本的 ari_cert_id）；已有进行中的订单时什么也不做
	// （ON CONFLICT ... DO NOTHING）。返回新订单 id 与是否新建。
	QueueOrder(ctx context.Context, certificateID uuid.UUID, reason string, createdBy *uuid.UUID) (uuid.UUID, bool, error)
	// ActiveOrder 读证书进行中的订单。
	ActiveOrder(ctx context.Context, certificateID uuid.UUID) (repo.OrderBriefRow, bool, error)
	// CancelQueuedOrders 把证书 queued 的订单转 cancelled（finished_at = now()，error_code = code）。
	CancelQueuedOrders(ctx context.Context, certificateID uuid.UUID, code string) error
	// QueueDue 给到期的证书排订单，返回新订单 id。到期：status 为 pending / active，没签出过或 renew_after 已过，
	// next_attempt_at 为空或已过，且没有进行中的订单；reason 没版本为 initial、有 ARI 窗口为 ari、否则 renewal；
	// 按 renew_after NULLS FIRST, id 取前 limit 张。
	QueueDue(ctx context.Context, limit int) ([]uuid.UUID, error)
	// OpenOrders 返回 queued 的订单 id，按 created_at, id 取前 limit 张。
	OpenOrders(ctx context.Context, limit int) ([]uuid.UUID, error)
	// ExpiredOrders 返回 running 但 lease_until 已过的订单（签发任务中断：进程退出、任务丢失），
	// 按 lease_until, id 取前 limit 张。
	ExpiredOrders(ctx context.Context, limit int) ([]repo.ClaimedOrder, error)
	// ClaimOrder 认领一张 queued 的订单：转 running、attempt + 1、lease_until = now() + lease、
	// started_at = now()。不是 queued（已被认领、已结束、不存在）时 ok=false。
	ClaimOrder(ctx context.Context, orderID uuid.UUID, lease time.Duration) (repo.ClaimedOrder, bool, error)
	// LockOwnedOrder 以 FOR UPDATE 锁住订单并确认它仍是 running 且 attempt 未变。
	LockOwnedOrder(ctx context.Context, orderID uuid.UUID, attempt int) (bool, error)
	// OrderWork 读签发一张订单要的证书侧信息。
	OrderWork(ctx context.Context, certificateID uuid.UUID) (repo.OrderWorkRow, bool, error)
	// FinishOrderSucceeded 订单转 succeeded（finished_at、version_id、ca、replaces_ari_id = replaces，空串写 NULL）。
	FinishOrderSucceeded(ctx context.Context, orderID, versionID uuid.UUID, ca, replaces string) error
	// FinishOrderFailed 订单转 state（failed / cancelled），写 finished_at、error_code、error_detail、ca（空串写 NULL）。
	FinishOrderFailed(ctx context.Context, orderID uuid.UUID, state, code, detail, ca string) error

	// --- 版本与签发结果 ---

	// NextVersion 是证书的下一个版本号（max(version) + 1，没有版本时 1）。
	NextVersion(ctx context.Context, certificateID uuid.UUID) (int, error)
	// InsertVersion 插入一个版本（ID 由调用方生成）。
	InsertVersion(ctx context.Context, v *model.CertificateVersion) error
	// ApplyIssued 把证书切到新版本：当前版本、not_before / not_after、renew_after，清 ARI 窗口与检查时间、
	// 失败计数、退避、最近错误、alerted_level；pending 转 active；row_version + 1。
	ApplyIssued(ctx context.Context, certificateID uuid.UUID, u repo.SuccessUpdate) error
	// ApplyFailure 记一次失败：consecutive_failures、next_attempt_at = now() + RetryIn、最近错误；Pause 为真时转 paused
	// （paused_reason = failures）。row_version + 1。
	ApplyFailure(ctx context.Context, certificateID uuid.UUID, u repo.FailureUpdate) error
	// InsertIssuance 写一行签发流水。
	InsertIssuance(ctx context.Context, i *model.CertificateIssuance) error
	// IssuancesSince 读 created_at > since 的签发流水。
	IssuancesSince(ctx context.Context, since time.Time) ([]repo.IssuanceRow, error)

	// --- ARI 与到期等级 ---

	// ARIDue 返回 status = active、ari_checked_at 为空或早于 now() - recheck 的证书与当前版本，
	// 按 ari_checked_at NULLS FIRST, id 取前 limit 张。
	ARIDue(ctx context.Context, recheck time.Duration, limit int) ([]repo.ARIDueRow, error)
	// RecordARI 写 ari_checked_at = now()；start、end、renewAt 非空时覆盖窗口与 renew_after。只在当前版本仍是
	// versionID 时写。
	RecordARI(ctx context.Context, certificateID, versionID uuid.UUID, start, end, renewAt *time.Time) error
	// AlertCandidates 返回有当前版本且 alerted_level < 2 的证书，按 not_after 取前 limit 张。
	AlertCandidates(ctx context.Context, limit int) ([]repo.AlertRow, error)
	// RaiseAlert 只在 alerted_level < level 时把它改成 level；返回是否改了。
	RaiseAlert(ctx context.Context, id uuid.UUID, level int) (bool, error)

	// --- ACME 账号 ---

	// ValidAccount 读（目录, 联系邮箱, EAB KID）对应的有效账号；eabKID 空串表示没有 EAB。
	ValidAccount(ctx context.Context, directory, email, eabKID string) (model.ACMEAccount, bool, error)
	// InsertAccountIfAbsent 插入账号；同一（目录, 邮箱, EAB KID）已有有效账号时什么也不做，返回是否插入。
	InsertAccountIfAbsent(ctx context.Context, a *model.ACMEAccount) (bool, error)
	// DeactivateAccount 把有效账号标 deactivated；返回是否改了。
	DeactivateAccount(ctx context.Context, id uuid.UUID) (bool, error)
}

var _ store = (*repo.Repo)(nil)
