package service

// 后台权限码。与迁移 00006_auth.sql 的 permissions 种子由 PG 测试对齐；
// 路由上写的权限码必须在这里登记，Permission 中间件遇到未登记的码在注册时 panic。
const (
	PermIAMUserRead  = "iam.user.read"
	PermIAMUserWrite = "iam.user.write"

	PermCatalogRead    = "catalog.read"
	PermCatalogWrite   = "catalog.write"
	PermCatalogPublish = "catalog.publish"

	PermBillingOrderRead       = "billing.order.read"
	PermBillingOrderWrite      = "billing.order.write"
	PermBillingPaymentRead     = "billing.payment.read"
	PermBillingLedgerRead      = "billing.ledger.read"
	PermBillingAdjustmentWrite = "billing.adjustment.write"
	PermBillingProviderWrite   = "billing.provider.write"

	PermNodeRead             = "node.read"
	PermNodeProvision        = "node.provision"
	PermNodeWrite            = "node.write"
	PermNodeLifecycle        = "node.lifecycle"
	PermNodeIdentityRevoke   = "node.identity.revoke"
	PermNodeConfigPublish    = "node.config.publish"
	PermNodeCertificateRead  = "node.certificate.read"
	PermNodeCertificateWrite = "node.certificate.write"

	PermMeteringRead       = "metering.read"
	PermMeteringResetRead  = "metering.reset.read"
	PermMeteringResetWrite = "metering.reset.write"

	PermOpsTicketRead          = "ops.ticket.read"
	PermOpsTicketWrite         = "ops.ticket.write"
	PermOpsAnnouncementWrite   = "ops.announcement.write"
	PermOpsNotificationRead    = "ops.notification.read"
	PermOpsNotificationWrite   = "ops.notification.write"
	PermOpsContentWrite        = "ops.content.write"
	PermOpsExport              = "ops.export"
	PermOpsDashboardRead       = "ops.dashboard.read"
	PermMarketingCouponRead    = "marketing.coupon.read"
	PermMarketingCouponWrite   = "marketing.coupon.write"
	PermMarketingGiftcardRead  = "marketing.giftcard.read"
	PermMarketingGiftcardWrite = "marketing.giftcard.write"

	PermMarketingCommissionRead    = "marketing.commission.read"
	PermMarketingCommissionWrite   = "marketing.commission.write"
	PermMarketingWithdrawalApprove = "marketing.withdrawal.approve"
	PermSecurityAuditRead          = "security.audit.read"
	PermSecurityRiskReview         = "security.risk.review"
	PermPlatformSettingsWrite      = "platform.settings.write"
	PermPlatformAppearanceRead     = "platform.appearance.read"
	PermPlatformAppearanceWrite    = "platform.appearance.write"
	PermPlatformPluginRead         = "platform.plugin.read"
	PermPlatformPluginWrite        = "platform.plugin.write"
)

// AdminPermission 决定谁算「有效管理员」：活跃用户经长期绑定持有这个码（最后一个管理员保护）。
const AdminPermission = PermIAMUserWrite

// 内置角色码（迁移种子）。
const (
	RolePlatformAdmin    = "platform_admin"
	RoleTenantAdmin      = "tenant_admin"
	RoleFinance          = "finance"
	RoleCustomerSupport  = "customer_support"
	RoleResourceOperator = "resource_operator"
	RoleSecurityAuditor  = "security_auditor"
)

var allPermissions = []string{
	PermIAMUserRead, PermIAMUserWrite,
	PermCatalogRead, PermCatalogWrite, PermCatalogPublish,
	PermBillingOrderRead, PermBillingOrderWrite, PermBillingPaymentRead, PermBillingLedgerRead,
	PermBillingAdjustmentWrite, PermBillingProviderWrite,
	PermNodeRead, PermNodeProvision, PermNodeWrite, PermNodeLifecycle, PermNodeIdentityRevoke,
	PermNodeConfigPublish, PermNodeCertificateRead, PermNodeCertificateWrite,
	PermMeteringRead, PermMeteringResetRead, PermMeteringResetWrite,
	PermOpsTicketRead, PermOpsTicketWrite, PermOpsAnnouncementWrite, PermOpsNotificationRead,
	PermOpsNotificationWrite, PermOpsContentWrite, PermOpsExport, PermOpsDashboardRead,
	PermMarketingCouponRead, PermMarketingCouponWrite, PermMarketingGiftcardRead, PermMarketingGiftcardWrite,
	PermMarketingCommissionRead, PermMarketingCommissionWrite, PermMarketingWithdrawalApprove,
	PermSecurityAuditRead, PermSecurityRiskReview,
	PermPlatformSettingsWrite, PermPlatformAppearanceRead, PermPlatformAppearanceWrite,
	PermPlatformPluginRead, PermPlatformPluginWrite,
}

var permissionSet = func() map[string]bool {
	m := make(map[string]bool, len(allPermissions))
	for _, c := range allPermissions {
		m[c] = true
	}
	return m
}()

// AllPermissions 返回全部已登记的权限码（副本）。
func AllPermissions() []string {
	return append([]string(nil), allPermissions...)
}

// IsPermission 报告 code 是否是已登记的权限码。
func IsPermission(code string) bool {
	return permissionSet[code]
}
