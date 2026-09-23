// Package iamguard contains tenant-scoped IAM invariants shared by HTTP
// administration and the local bootstrap CLI.
package iamguard

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

var ErrLastEffectiveAdministrator = httpx.New(httpx.CodeConflict,
	"每个租户必须保留至少一个可登录的有效管理员")

var ErrNotEffectiveAdministrator = httpx.New(httpx.CodeForbidden,
	"目标账号不是可登录的永久租户管理员")

// LockLastAdministrator serializes every mutation that could change the set of
// effective tenant administrators. A real tenant row is the common lock anchor
// for application paths and the future database trigger defense.
func LockLastAdministrator(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var lockedTenant string
	return tx.QueryRow(ctx,
		`SELECT id::text FROM tenants WHERE id = $1 FOR UPDATE`, tenantID).Scan(&lockedTenant)
}

// RequireEffectiveAdministrator checks the post-mutation state while the
// caller still holds LockLastAdministrator. Permissions may be composed from
// multiple permanent tenant-scoped bindings, matching the runtime union model
// without trusting naturally expiring grants.
func RequireEffectiveAdministrator(ctx context.Context, tx pgx.Tx, tenantID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM users u
			 WHERE u.tenant_id = $1
			   AND u.status = 'active'
			   AND EXISTS (
				 SELECT 1
				   FROM role_bindings rb
				   JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
				   JOIN role_permissions rp ON rp.role_id = rb.role_id
				  WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id
				    AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
				    AND rb.expires_at IS NULL
				    AND rp.permission_code = 'iam.user.write')
			   AND EXISTS (
				 SELECT 1
				   FROM role_bindings rb
				   JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
				   JOIN role_permissions rp ON rp.role_id = rb.role_id
				  WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id
				    AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
				    AND rb.expires_at IS NULL
				    AND rp.permission_code = 'iam.role.write'))`, tenantID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrLastEffectiveAdministrator
	}
	return nil
}

// RequireUserEffectiveAdministrator applies the same durable administrator
// definition to one locked account. Operator-only credential rotation uses it
// to avoid becoming a general-purpose password reset primitive for users.
func RequireUserEffectiveAdministrator(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			  FROM users u
			 WHERE u.tenant_id = $1 AND u.id = $2::uuid
			   AND u.status = 'active'
			   AND EXISTS (
				 SELECT 1
				   FROM role_bindings rb
				   JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
				   JOIN role_permissions rp ON rp.role_id = rb.role_id
				  WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id
				    AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
				    AND rb.expires_at IS NULL
				    AND rp.permission_code = 'iam.user.write')
			   AND EXISTS (
				 SELECT 1
				   FROM role_bindings rb
				   JOIN roles r ON r.id = rb.role_id AND r.tenant_id = rb.tenant_id
				   JOIN role_permissions rp ON rp.role_id = rb.role_id
				  WHERE rb.tenant_id = u.tenant_id AND rb.user_id = u.id
				    AND rb.scope_type = 'tenant' AND rb.scope_id IS NULL
				    AND rb.expires_at IS NULL
				    AND rp.permission_code = 'iam.role.write'))`, tenantID, userID).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotEffectiveAdministrator
	}
	return nil
}
