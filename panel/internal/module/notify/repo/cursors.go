package repo

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// ScanCursor 是一种扫描游标：已处理到 (At, ID)。
type ScanCursor struct {
	At time.Time `gorm:"column:last_at"`
	ID uuid.UUID `gorm:"column:last_id"`
}

// ScanCursor 读游标；没有时 ok 为 false。
func (r *Repo) ScanCursor(ctx context.Context, name string) (ScanCursor, bool, error) {
	var c ScanCursor
	res := r.d.Conn(ctx).Raw(`SELECT last_at, last_id FROM notify_scan_cursors WHERE name = ?`, name).Scan(&c)
	if res.Error != nil {
		return ScanCursor{}, false, res.Error
	}
	return c, res.RowsAffected > 0, nil
}

// SetScanCursor 写游标（没有就建）。
func (r *Repo) SetScanCursor(ctx context.Context, name string, c ScanCursor) error {
	return r.d.Conn(ctx).Exec(`
		INSERT INTO notify_scan_cursors (name, last_at, last_id) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET last_at = EXCLUDED.last_at, last_id = EXCLUDED.last_id, updated_at = now()`,
		name, c.At, c.ID).Error
}
