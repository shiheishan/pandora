package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// Options 打开 PostgreSQL 连接时的参数。
type Options struct {
	URL           string
	MaxConns      int
	MinConns      int
	Logger        *zap.Logger
	SlowThreshold time.Duration
}

// Open 打开 GORM 连接并包装为 *DB。
func Open(ctx context.Context, o Options) (*DB, error) {
	threshold := o.SlowThreshold
	if threshold == 0 {
		threshold = 200 * time.Millisecond
	}

	g, err := gorm.Open(postgres.New(postgres.Config{DSN: o.URL}), &gorm.Config{
		TranslateError:         true,
		SkipDefaultTransaction: true,
		PrepareStmt:            false,
		NowFunc:                func() time.Time { return time.Now().UTC() },
		Logger:                 newGormLogger(o.Logger, threshold),
	})
	if err != nil {
		return nil, err
	}

	sqlDB, err := g.DB()
	if err != nil {
		return nil, err
	}

	sqlDB.SetMaxOpenConns(o.MaxConns)
	idle := o.MinConns
	if idle == 0 {
		idle = 2
	}
	sqlDB.SetMaxIdleConns(idle)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)

	if err := sqlDB.PingContext(ctx); err != nil {
		_ = sqlDB.Close()
		return nil, err
	}

	return Wrap(g), nil
}

// Close 关闭底层连接池。
func (d *DB) Close() error {
	sqlDB, err := d.base.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Ping 检查数据库是否可达。
func (d *DB) Ping(ctx context.Context) error {
	sqlDB, err := d.base.DB()
	if err != nil {
		return err
	}
	return sqlDB.PingContext(ctx)
}

// CheckRuntimeRole 确认当前库角色不是超级用户且不能建角色、建库（仅 production 启动时调用）。
func (d *DB) CheckRuntimeRole(ctx context.Context) error {
	var super, createrole, createdb bool
	err := d.Conn(ctx).Raw(
		`SELECT rolsuper, rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = current_user`,
	).Row().Scan(&super, &createrole, &createdb)
	if err != nil {
		return fmt.Errorf("查询运行角色权限: %w", err)
	}
	if super || createrole || createdb {
		return errors.New("运行角色不能是超级用户，也不能有建角色、建库权限")
	}
	return nil
}
