// Package migrations 是新栈的 goose 迁移，SQL 文件嵌入二进制，由 pandora migrate 执行。
//
// 每个迁移都必须有 Down；确实不可逆的在 Down 里写明原因。CI 跑 up → down 到底 → up。
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// Version 是一个迁移的状态。
type Version struct {
	Version int64
	Name    string
	Applied bool
}

func provider(db *sql.DB) (*goose.Provider, error) {
	return goose.NewProvider(goose.DialectPostgres, db, files)
}

// Up 执行全部未执行的迁移。
func Up(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.Up(ctx)
	return err
}

// DownOne 回退一个版本。
func DownOne(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.Down(ctx)
	return err
}

// DownAll 回退到 0，只给 CI 往返与测试用。
func DownAll(ctx context.Context, db *sql.DB) error {
	p, err := provider(db)
	if err != nil {
		return err
	}
	_, err = p.DownTo(ctx, 0)
	return err
}

// Status 返回每个迁移是否已执行。
func Status(ctx context.Context, db *sql.DB) ([]Version, error) {
	p, err := provider(db)
	if err != nil {
		return nil, err
	}
	st, err := p.Status(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Version, 0, len(st))
	for _, s := range st {
		out = append(out, Version{
			Version: s.Source.Version,
			Name:    strings.TrimSuffix(filepath.Base(s.Source.Path), ".sql"),
			Applied: s.State == goose.StateApplied,
		})
	}
	return out, nil
}
