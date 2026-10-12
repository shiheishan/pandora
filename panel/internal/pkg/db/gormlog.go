package db

import (
	"context"
	"errors"
	"time"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// gormZapLogger 把 GORM SQL 日志接到 zap。
type gormZapLogger struct {
	base          *zap.Logger
	slowThreshold time.Duration
	level         gormlogger.LogLevel
}

func newGormLogger(base *zap.Logger, slowThreshold time.Duration) gormlogger.Interface {
	if base == nil {
		base = zap.L()
	}
	return &gormZapLogger{
		base:          base,
		slowThreshold: slowThreshold,
		level:         gormlogger.Warn,
	}
}

func (l *gormZapLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	cp := *l
	cp.level = level
	return &cp
}

func (l *gormZapLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	plog.From(ctx).Sugar().Infof(msg, data...)
}

func (l *gormZapLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	plog.From(ctx).Sugar().Warnf(msg, data...)
}

func (l *gormZapLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	plog.From(ctx).Sugar().Errorf(msg, data...)
}

func (l *gormZapLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		sql, rows := fc()
		dur := time.Since(begin)
		plog.From(ctx).Error("sql",
			zap.String("sql", sql),
			zap.Int64("rows", rows),
			zap.Int64("dur_ms", dur.Milliseconds()),
			zap.Error(err),
		)
		return
	}
	dur := time.Since(begin)
	if dur > l.slowThreshold {
		sql, rows := fc()
		plog.From(ctx).Warn("slow sql",
			zap.String("sql", sql),
			zap.Int64("rows", rows),
			zap.Int64("dur_ms", dur.Milliseconds()),
		)
	}
}
