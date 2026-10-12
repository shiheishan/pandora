// Package log 封装 zap：按配置建 logger、ctx 传递、请求 ID 字段与写出前脱敏。
package log

import (
	"context"
	"fmt"
	"os"

	"github.com/aegispanel/aegis/internal/pkg/config"
	"github.com/aegispanel/aegis/internal/pkg/reqid"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

type ctxKey struct{}

// New 按日志配置与环境名构造 logger。
func New(cfg config.Log, env string) (*zap.Logger, error) {
	lvl, err := zapcore.ParseLevel(cfg.Level)
	if err != nil {
		return nil, fmt.Errorf("解析日志级别: %w", err)
	}

	encCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stack",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var enc zapcore.Encoder
	if env == config.EnvDevelopment {
		encCfg.EncodeLevel = zapcore.LowercaseColorLevelEncoder
		enc = zapcore.NewConsoleEncoder(encCfg)
	} else {
		enc = zapcore.NewJSONEncoder(encCfg)
	}

	var ws zapcore.WriteSyncer
	if cfg.File != "" {
		ws = zapcore.AddSync(&lumberjack.Logger{
			Filename:   cfg.File,
			MaxSize:    cfg.MaxSizeMB,
			MaxBackups: cfg.MaxBackups,
			MaxAge:     cfg.MaxAgeDays,
			Compress:   cfg.Compress,
		})
	} else {
		ws = zapcore.AddSync(os.Stderr)
	}

	core := zapcore.NewCore(enc, ws, lvl)
	core = NewRedactingCore(core)

	logger := zap.New(core,
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	return logger, nil
}

// With 把 logger 放进 ctx。
func With(ctx context.Context, l *zap.Logger) context.Context {
	return context.WithValue(ctx, ctxKey{}, l)
}

// From 从 ctx 取 logger；没有则返回 zap.L()。
func From(ctx context.Context) *zap.Logger {
	if l, ok := ctx.Value(ctxKey{}).(*zap.Logger); ok && l != nil {
		return l
	}
	return zap.L()
}

// WithReqID 同时写入 reqid 与带 req_id 字段的 logger。
func WithReqID(ctx context.Context, base *zap.Logger, id string) context.Context {
	ctx = reqid.With(ctx, id)
	return With(ctx, base.With(zap.String("req_id", id)))
}
