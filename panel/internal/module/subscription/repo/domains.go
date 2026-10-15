package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/subscription/model"
)

// DomainGroupRow 是 subscription_domain_groups 表读取时的一行。
type DomainGroupRow struct {
	ID        uuid.UUID `gorm:"column:id"`
	Name      string    `gorm:"column:name"`
	IsDefault bool      `gorm:"column:is_default"`
	Position  int       `gorm:"column:position"`
}

// DomainRow 是 subscription_domains 表读取时的一行。
type DomainRow struct {
	GroupID  uuid.UUID `gorm:"column:group_id"`
	BaseURL  string    `gorm:"column:base_url"`
	Weight   int       `gorm:"column:weight"`
	Position int       `gorm:"column:position"`
}

// DomainAssignRow 是 subscription_domain_group_user_groups 表读取时的一行。
type DomainAssignRow struct {
	UserGroupID uuid.UUID `gorm:"column:user_group_id"`
	GroupID     uuid.UUID `gorm:"column:group_id"`
}

// DomainConfigRows 是整份订阅域名配置的扁平行结构。
type DomainConfigRows struct {
	Groups  []DomainGroupRow
	Domains []DomainRow
	Assigns []DomainAssignRow
}

// DomainConfig 读出整份订阅域名分组、域名及用户组分配。
// 拉取侧经 service 的进程内快照缓存，不是每次拉取都查。
func (r *Repo) DomainConfig(ctx context.Context) (DomainConfigRows, error) {
	var rows DomainConfigRows
	if err := r.d.Conn(ctx).Raw("SELECT id, name, is_default, position FROM subscription_domain_groups ORDER BY position").Scan(&rows.Groups).Error; err != nil {
		return DomainConfigRows{}, mapErr(err)
	}
	if err := r.d.Conn(ctx).Raw("SELECT group_id, base_url, weight, position FROM subscription_domains ORDER BY group_id, position").Scan(&rows.Domains).Error; err != nil {
		return DomainConfigRows{}, mapErr(err)
	}
	if err := r.d.Conn(ctx).Raw("SELECT user_group_id, group_id FROM subscription_domain_group_user_groups ORDER BY group_id, user_group_id::text").Scan(&rows.Assigns).Error; err != nil {
		return DomainConfigRows{}, mapErr(err)
	}
	if rows.Groups == nil {
		rows.Groups = []DomainGroupRow{}
	}
	if rows.Domains == nil {
		rows.Domains = []DomainRow{}
	}
	if rows.Assigns == nil {
		rows.Assigns = []DomainAssignRow{}
	}
	return rows, nil
}

// LockDomainConfig 在当前事务里加咨询锁，串行化后台的整份保存。
func (r *Repo) LockDomainConfig(ctx context.Context) error {
	err := r.d.Conn(ctx).Exec(
		"SELECT pg_advisory_xact_lock(hashtextextended(?, 0))",
		"subscription_domains",
	).Error
	return mapErr(err)
}

// ReplaceDomainConfig 整份替换订阅域名分组配置：先清空组（外键 CASCADE 级联删除域名与分配），
// 再依次插入新组、域名与用户组分配。
// 调用方必须在事务里、先 LockDomainConfig。
func (r *Repo) ReplaceDomainConfig(ctx context.Context, rows DomainConfigRows) error {
	if err := r.d.Conn(ctx).Exec("DELETE FROM subscription_domain_groups").Error; err != nil {
		return mapErr(err)
	}

	if len(rows.Groups) > 0 {
		groups := make([]model.DomainGroup, len(rows.Groups))
		for i, g := range rows.Groups {
			groups[i] = model.DomainGroup{
				ID:        g.ID,
				Name:      g.Name,
				IsDefault: g.IsDefault,
				Position:  g.Position,
			}
		}
		if err := r.d.Conn(ctx).Create(&groups).Error; err != nil {
			return mapErr(err)
		}
	}

	if len(rows.Domains) > 0 {
		domains := make([]model.Domain, len(rows.Domains))
		for i, d := range rows.Domains {
			domains[i] = model.Domain{
				GroupID:  d.GroupID,
				BaseURL:  d.BaseURL,
				Weight:   d.Weight,
				Position: d.Position,
			}
		}
		if err := r.d.Conn(ctx).Create(&domains).Error; err != nil {
			return mapErr(err)
		}
	}

	if len(rows.Assigns) > 0 {
		assigns := make([]model.DomainGroupUserGroup, len(rows.Assigns))
		for i, a := range rows.Assigns {
			assigns[i] = model.DomainGroupUserGroup{
				UserGroupID: a.UserGroupID,
				GroupID:     a.GroupID,
			}
		}
		if err := r.d.Conn(ctx).Create(&assigns).Error; err != nil {
			return mapErr(err)
		}
	}

	return nil
}
