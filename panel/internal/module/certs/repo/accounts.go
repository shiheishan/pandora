package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/certs/model"
)

// ValidAccount 读（目录, 联系邮箱, EAB KID）对应的有效账号；eabKID 空串表示没有 EAB。
func (r *Repo) ValidAccount(ctx context.Context, directory, email, eabKID string) (model.ACMEAccount, bool, error) {
	var a model.ACMEAccount
	err := r.d.Conn(ctx).Raw(`
		SELECT id, ca, directory_url, contact_email, account_key_sealed, account_url, eab_kid, status, created_at, updated_at
		  FROM acme_accounts
		 WHERE directory_url = ? AND contact_email = ? AND coalesce(eab_kid, '') = ? AND status = 'valid'`,
		directory, email, eabKID).Scan(&a).Error
	if err != nil {
		return model.ACMEAccount{}, false, err
	}
	if a.ID == uuid.Nil {
		return model.ACMEAccount{}, false, nil
	}
	return a, true, nil
}

// InsertAccountIfAbsent 插入账号；已有有效账号时什么也不做，返回是否插入。
func (r *Repo) InsertAccountIfAbsent(ctx context.Context, a *model.ACMEAccount) (bool, error) {
	var eab any
	if a.EABKID != nil {
		eab = *a.EABKID
	}
	res := r.d.Conn(ctx).Exec(`
		INSERT INTO acme_accounts (id, ca, directory_url, contact_email, account_url, eab_kid, account_key_sealed)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (directory_url, contact_email, (coalesce(eab_kid, ''))) WHERE status = 'valid' DO NOTHING`,
		a.ID, a.CA, a.DirectoryURL, a.ContactEmail, a.AccountURL, eab, a.AccountKeySealed)
	if res.Error != nil {
		return false, mapErr(res.Error)
	}
	return res.RowsAffected == 1, nil
}

// DeactivateAccount 把有效账号标 deactivated；返回是否改了。
func (r *Repo) DeactivateAccount(ctx context.Context, id uuid.UUID) (bool, error) {
	res := r.d.Conn(ctx).Exec(`
		UPDATE acme_accounts SET status = 'deactivated', updated_at = now()
		 WHERE id = ? AND status = 'valid'`, id)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}
