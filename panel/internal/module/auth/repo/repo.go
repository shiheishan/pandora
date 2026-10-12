// Package repo 是 auth 模块唯一使用 GORM 的地方。
package repo

import (
	"context"

	"github.com/aegispanel/aegis/internal/pkg/db"
	"github.com/google/uuid"
)

// adminSetLockKey 是最后一个管理员保护的锁锚；所有改管理员集合的路径都先取它，只在事务里调用。
const adminSetLockKey int64 = 0x70616e646f7261

const unexpiredUR = "(ur.expires_at IS NULL OR ur.expires_at > now())"

// Repo 是 auth 相关表的 GORM 访问入口。
type Repo struct{ d *db.DB }

// New 构造 Repo。
func New(d *db.DB) *Repo { return &Repo{d: d} }

// RoleRow 是一个角色的标识与名称。
type RoleRow struct {
	ID   uuid.UUID
	Code string
	Name string
}

// HolderRow 是持有未过期角色绑定的一个用户：Roles 是逗号分隔、升序、去重的角色码，Permissions 是去重后的权限数。
type HolderRow struct {
	UserID      uuid.UUID
	Roles       string
	Permissions int
}

// RoleSummaryRow 是一个角色及其权限数。
type RoleSummaryRow struct {
	Code        string
	Name        string
	Permissions int
}

// Permissions 返回用户当前生效（未过期绑定）的权限码，去重、升序。
func (r *Repo) Permissions(ctx context.Context, userID uuid.UUID) ([]string, error) {
	var codes []string
	err := r.d.Conn(ctx).Raw(
		"SELECT DISTINCT rp.permission_code FROM user_roles ur JOIN role_permissions rp ON rp.role_id = ur.role_id WHERE ur.user_id = ? AND "+unexpiredUR+" ORDER BY 1",
		userID,
	).Scan(&codes).Error
	if err != nil {
		return nil, err
	}
	if codes == nil {
		return []string{}, nil
	}
	return codes, nil
}

// HasActiveRole 报告用户是否持有任何未过期的角色绑定。
func (r *Repo) HasActiveRole(ctx context.Context, userID uuid.UUID) (bool, error) {
	var ok bool
	err := r.d.Conn(ctx).Raw(
		"SELECT EXISTS (SELECT 1 FROM user_roles ur WHERE ur.user_id = ? AND "+unexpiredUR+")",
		userID,
	).Scan(&ok).Error
	return ok, err
}

// UserRoles 返回用户未过期绑定的角色（按 name、code 排序）。
func (r *Repo) UserRoles(ctx context.Context, userID uuid.UUID) ([]RoleRow, error) {
	var rows []RoleRow
	err := r.d.Conn(ctx).Raw(
		"SELECT r.id, r.code, r.name FROM user_roles ur JOIN roles r ON r.id = ur.role_id WHERE ur.user_id = ? AND "+unexpiredUR+" ORDER BY r.name, r.code",
		userID,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []RoleRow{}, nil
	}
	return rows, nil
}

// RoleByCode 按角色码取角色；不存在 ok 为 false。
func (r *Repo) RoleByCode(ctx context.Context, code string) (RoleRow, bool, error) {
	var row RoleRow
	err := r.d.Conn(ctx).Raw(
		"SELECT id, code, name FROM roles WHERE code = ?",
		code,
	).Scan(&row).Error
	if err != nil {
		return RoleRow{}, false, err
	}
	if row.ID == uuid.Nil {
		return RoleRow{}, false, nil
	}
	return row, true, nil
}

// UpsertUserRole 授予（或把已有绑定改成长期绑定）。
func (r *Repo) UpsertUserRole(ctx context.Context, userID, roleID uuid.UUID, grantedBy *uuid.UUID) error {
	return r.d.Conn(ctx).Exec(
		"INSERT INTO user_roles (user_id, role_id, expires_at, granted_by) VALUES (?, ?, NULL, ?) ON CONFLICT (user_id, role_id) DO UPDATE SET expires_at = NULL, granted_by = EXCLUDED.granted_by",
		userID, roleID, grantedBy,
	).Error
}

// DeleteUserRole 收回绑定；本来没有返回 false。
func (r *Repo) DeleteUserRole(ctx context.Context, userID, roleID uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(
		"DELETE FROM user_roles WHERE user_id = ? AND role_id = ?",
		userID, roleID,
	)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// LockAdminSet 在当前事务里取最后一个管理员保护的事务级咨询锁。
func (r *Repo) LockAdminSet(ctx context.Context) error {
	return r.d.Conn(ctx).Exec("SELECT pg_advisory_xact_lock(?)", adminSetLockKey).Error
}

// PermanentHolders 返回经长期绑定持有 permission 的用户（去重）。
func (r *Repo) PermanentHolders(ctx context.Context, permission string) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.d.Conn(ctx).Raw(
		"SELECT DISTINCT ur.user_id FROM user_roles ur JOIN role_permissions rp ON rp.role_id = ur.role_id WHERE rp.permission_code = ? AND ur.expires_at IS NULL ORDER BY 1",
		permission,
	).Scan(&ids).Error
	if err != nil {
		return nil, err
	}
	if ids == nil {
		return []uuid.UUID{}, nil
	}
	return ids, nil
}

// RoleHolders 返回持有未过期绑定的用户及其角色码、权限数。
func (r *Repo) RoleHolders(ctx context.Context) ([]HolderRow, error) {
	var rows []HolderRow
	err := r.d.Conn(ctx).Raw(
		`SELECT ur.user_id,
	       string_agg(DISTINCT r.code, ',' ORDER BY r.code) AS roles,
	       count(DISTINCT rp.permission_code) AS permissions
	  FROM user_roles ur
	  JOIN roles r ON r.id = ur.role_id
	  LEFT JOIN role_permissions rp ON rp.role_id = ur.role_id
	 WHERE ` + unexpiredUR + `
	 GROUP BY ur.user_id
	 ORDER BY ur.user_id`,
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []HolderRow{}, nil
	}
	return rows, nil
}

// Roles 返回全部角色及权限数（按 code 排序）。
func (r *Repo) Roles(ctx context.Context) ([]RoleSummaryRow, error) {
	var rows []RoleSummaryRow
	err := r.d.Conn(ctx).Raw(
		"SELECT r.code, r.name, count(rp.id) AS permissions FROM roles r LEFT JOIN role_permissions rp ON rp.role_id = r.id GROUP BY r.id, r.code, r.name ORDER BY r.code",
	).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	if rows == nil {
		return []RoleSummaryRow{}, nil
	}
	return rows, nil
}

// PermissionCodes 返回权限字典全部码（升序），供种子对齐测试用。
func (r *Repo) PermissionCodes(ctx context.Context) ([]string, error) {
	var codes []string
	err := r.d.Conn(ctx).Raw("SELECT code FROM permissions ORDER BY code").Scan(&codes).Error
	if err != nil {
		return nil, err
	}
	if codes == nil {
		return []string{}, nil
	}
	return codes, nil
}
