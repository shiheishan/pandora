package db

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"gorm.io/gorm"
)

// 日志里的 SQL 不带参数值：GORM 发现 logger 实现了 ParamsFilter 就用它过滤后再渲染。
func TestGormLoggerDropsParams(t *testing.T) {
	l := newGormLogger(zap.NewNop(), 0)
	f, ok := l.(gorm.ParamsFilter)
	if !ok {
		t.Fatal("gorm logger 必须实现 ParamsFilter")
	}
	sql, vars := f.ParamsFilter(context.Background(), "SELECT 1 WHERE email = $1", "user@example.com")
	if sql != "SELECT 1 WHERE email = $1" || vars != nil {
		t.Fatalf("got %q %v", sql, vars)
	}
}
