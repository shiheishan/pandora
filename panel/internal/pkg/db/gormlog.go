package db

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const maxLoggedSQL = 2048

func init() {
	// Raw().Scan() 经 Recorder 临时替换 logger，会绕过 logger 自己的 ParamsFilter。
	// GORM Recorder 缺省原样返回参数，在此将其全局设为丢弃参数值，防止参数（敏感信息、批量 UUID 等）渲染进慢查询或错误日志。
	gormlogger.RecorderParamsFilter = func(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
		return sql, nil
	}
}

// clipSQL 截短过长的 SQL 语句，避免超长 SQL 撑大日志。
// 不超过 maxLoggedSQL 字节原样返回；超过时截到上限（按 UTF-8 字符边界往回退），后缀附加原字节数。
func clipSQL(s string) string {
	if len(s) <= maxLoggedSQL {
		return s
	}
	end := maxLoggedSQL
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return fmt.Sprintf("%s…(共 %d 字节)", s[:end], len(s))
}

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

// ParamsFilter 让日志里的 SQL 只带占位符、不带参数值：邮箱、令牌、回调地址、密文都可能是参数，
// 出错或慢查询时整条 SQL 会进日志。实现 gorm 的 ParamsFilter 接口，GORM 渲染 SQL 前调用。
func (l *gormZapLogger) ParamsFilter(_ context.Context, sql string, _ ...interface{}) (string, []interface{}) {
	return sql, nil
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
			zap.String("sql", clipSQL(sql)),
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
			zap.String("sql", clipSQL(sql)),
			zap.Int64("rows", rows),
			zap.Int64("dur_ms", dur.Milliseconds()),
		)
	}
}
