package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type orderIDRow struct {
	ID uuid.UUID `gorm:"column:id"`
}

// LockActiveOrders 以 FOR UPDATE 锁住证书进行中（queued / running）的订单并返回其中是否有 running。
func (r *Repo) LockActiveOrders(ctx context.Context, certificateID uuid.UUID) (bool, error) {
	var running bool
	err := r.d.Conn(ctx).Raw(`
		SELECT coalesce(bool_or(state = 'running'), false) AS running
		  FROM (
			SELECT state FROM certificate_orders
			 WHERE certificate_id = ? AND state IN ('queued', 'running')
			 FOR UPDATE) o`, certificateID).Scan(&running).Error
	return running, err
}

// QueueOrder 给证书排一张订单；已有进行中的订单时什么也不做，返回新订单 id 与是否新建。
func (r *Repo) QueueOrder(ctx context.Context, certificateID uuid.UUID, reason string, createdBy *uuid.UUID) (uuid.UUID, bool, error) {
	var rows []orderIDRow
	err := r.d.Conn(ctx).Raw(`
		INSERT INTO certificate_orders (certificate_id, reason, replaces_ari_id, created_by)
		SELECT c.id, ?, v.ari_cert_id, ?
		  FROM certificates c
		  LEFT JOIN certificate_versions v ON v.id = c.current_version_id
		 WHERE c.id = ?
		ON CONFLICT (certificate_id) WHERE state IN ('queued', 'running') DO NOTHING
		RETURNING id`, reason, createdBy, certificateID).Scan(&rows).Error
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(rows) == 0 {
		return uuid.Nil, false, nil
	}
	return rows[0].ID, true, nil
}

// ActiveOrder 读证书进行中的订单。
func (r *Repo) ActiveOrder(ctx context.Context, certificateID uuid.UUID) (OrderBriefRow, bool, error) {
	var row OrderBriefRow
	err := r.d.Conn(ctx).Raw(`
		SELECT id, state, reason, created_at FROM certificate_orders
		 WHERE certificate_id = ? AND state IN ('queued', 'running')
		 LIMIT 1`, certificateID).Scan(&row).Error
	if err != nil {
		return OrderBriefRow{}, false, err
	}
	if row.ID == uuid.Nil {
		return OrderBriefRow{}, false, nil
	}
	return row, true, nil
}

// CancelQueuedOrders 把证书 queued 的订单转 cancelled。
func (r *Repo) CancelQueuedOrders(ctx context.Context, certificateID uuid.UUID, code string) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificate_orders
		   SET state = 'cancelled', finished_at = now(), error_code = ?, updated_at = now()
		 WHERE certificate_id = ? AND state = 'queued'`, code, certificateID)
	return res.Error
}

// QueueDue 给到期的证书排订单，返回新订单 id 列表。
func (r *Repo) QueueDue(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var rows []orderIDRow
	err := r.d.Conn(ctx).Raw(`
		INSERT INTO certificate_orders (certificate_id, reason, replaces_ari_id)
		SELECT c.id,
		       CASE WHEN c.current_version_id IS NULL THEN 'initial'
		            WHEN c.ari_window_start IS NOT NULL THEN 'ari' ELSE 'renewal' END,
		       v.ari_cert_id
		  FROM certificates c
		  LEFT JOIN certificate_versions v ON v.id = c.current_version_id
		 WHERE c.status IN ('pending', 'active')
		   AND (c.current_version_id IS NULL OR c.renew_after <= now())
		   AND (c.next_attempt_at IS NULL OR c.next_attempt_at <= now())
		   AND NOT EXISTS (SELECT 1 FROM certificate_orders o
		                    WHERE o.certificate_id = c.id AND o.state IN ('queued', 'running'))
		 ORDER BY c.renew_after NULLS FIRST, c.id
		 LIMIT ?
		ON CONFLICT (certificate_id) WHERE state IN ('queued', 'running') DO NOTHING
		RETURNING id`, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

// OpenOrders 返回 queued 的订单 id，按 created_at, id 取前 limit 张。
func (r *Repo) OpenOrders(ctx context.Context, limit int) ([]uuid.UUID, error) {
	var rows []orderIDRow
	err := r.d.Conn(ctx).Raw(`
		SELECT id FROM certificate_orders
		 WHERE state = 'queued'
		 ORDER BY created_at, id
		 LIMIT ?`, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	ids := make([]uuid.UUID, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	return ids, nil
}

// ExpiredOrders 返回 running 但 lease_until 已过的订单，按 lease_until, id 取前 limit 张。
func (r *Repo) ExpiredOrders(ctx context.Context, limit int) ([]ClaimedOrder, error) {
	var rows []ClaimedOrder
	err := r.d.Conn(ctx).Raw(`
		SELECT id, certificate_id, reason, replaces_ari_id, attempt FROM certificate_orders
		 WHERE state = 'running' AND lease_until < now()
		 ORDER BY lease_until, id
		 LIMIT ?`, limit).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ClaimOrder 认领一张 queued 的订单；没认领到 ok=false。
func (r *Repo) ClaimOrder(ctx context.Context, orderID uuid.UUID, lease time.Duration) (ClaimedOrder, bool, error) {
	var row ClaimedOrder
	res := r.d.Conn(ctx).Raw(`
		UPDATE certificate_orders
		   SET state = 'running', attempt = attempt + 1, lease_until = now() + make_interval(secs => ?),
		       started_at = now(), updated_at = now()
		 WHERE id = ? AND state = 'queued'
		RETURNING id, certificate_id, reason, replaces_ari_id, attempt`, lease.Seconds(), orderID).Scan(&row)
	if res.Error != nil {
		return ClaimedOrder{}, false, res.Error
	}
	if res.RowsAffected == 0 {
		return ClaimedOrder{}, false, nil
	}
	return row, true, nil
}

// LockOwnedOrder 以 FOR UPDATE 锁住订单并确认它仍是 running 且 attempt 未变。
func (r *Repo) LockOwnedOrder(ctx context.Context, orderID uuid.UUID, attempt int) (bool, error) {
	var mine bool
	err := r.d.Conn(ctx).Raw(`
		SELECT state = 'running' AND attempt = ? AS mine
		  FROM certificate_orders WHERE id = ? FOR UPDATE`, attempt, orderID).Scan(&mine).Error
	if err != nil {
		return false, err
	}
	return mine, nil
}

// OrderWork 读签发一张订单要的证书侧信息。
func (r *Repo) OrderWork(ctx context.Context, certificateID uuid.UUID) (OrderWorkRow, bool, error) {
	var row OrderWorkRow
	res := r.d.Conn(ctx).Raw(`
		SELECT c.name, c.identifiers::text AS identifiers, c.key_type, c.status, c.dns_credential_id,
		       v.identifiers::text AS current_identifiers, v.ca AS current_ca,
		       v.acme_account_id AS current_account_id, c.not_after
		  FROM certificates c
		  LEFT JOIN certificate_versions v ON v.id = c.current_version_id
		 WHERE c.id = ?`, certificateID).Scan(&row)
	if res.Error != nil {
		return OrderWorkRow{}, false, res.Error
	}
	if res.RowsAffected == 0 {
		return OrderWorkRow{}, false, nil
	}
	return row, true, nil
}

// FinishOrderSucceeded 订单转 succeeded。
func (r *Repo) FinishOrderSucceeded(ctx context.Context, orderID, versionID uuid.UUID, ca, replaces string) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificate_orders
		   SET state = 'succeeded', finished_at = now(), version_id = ?, ca = ?,
		       replaces_ari_id = nullif(?, ''), updated_at = now()
		 WHERE id = ?`, versionID, ca, replaces, orderID)
	return res.Error
}

// FinishOrderFailed 订单转 state（failed / cancelled），写错误信息与 ca。
func (r *Repo) FinishOrderFailed(ctx context.Context, orderID uuid.UUID, state, code, detail, ca string) error {
	res := r.d.Conn(ctx).Exec(`
		UPDATE certificate_orders
		   SET state = ?, finished_at = now(), error_code = ?, error_detail = ?,
		       ca = nullif(?, ''), updated_at = now()
		 WHERE id = ?`, state, code, detail, ca, orderID)
	return res.Error
}
