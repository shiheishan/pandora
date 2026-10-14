package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
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

func TestRecorderDropsParams(t *testing.T) {
	ctx := context.Background()
	// Raw().Scan() 临时换上的就是这个 Recorder，它的 ParamsFilter 走包级 RecorderParamsFilter
	var filter gorm.ParamsFilter = gormlogger.Recorder.New()
	sql, vars := filter.ParamsFilter(ctx, "SELECT $1", "user@example.com")
	if sql != "SELECT $1" || vars != nil {
		t.Fatalf("got sql=%q, vars=%v; want sql=%q, vars=nil", sql, vars, "SELECT $1")
	}
}

func TestClipSQL(t *testing.T) {
	// 短串原样
	short := "SELECT * FROM users WHERE id = 1"
	if got := clipSQL(short); got != short {
		t.Fatalf("短串应保持原样，got %q", got)
	}

	// 恰好等于 2048 字节原样
	exact := strings.Repeat("a", maxLoggedSQL)
	if got := clipSQL(exact); got != exact {
		t.Fatalf("2048字节应保持原样，got len %d", len(got))
	}

	// 3000 字节 ASCII 截后以 …(共 3000 字节) 结尾且前缀长 2048
	longASCII := strings.Repeat("b", 3000)
	gotASCII := clipSQL(longASCII)
	expectedSuffix := fmt.Sprintf("…(共 %d 字节)", len(longASCII))
	if !strings.HasSuffix(gotASCII, expectedSuffix) {
		t.Fatalf("截后后缀不匹配，got %q, want suffix %q", gotASCII, expectedSuffix)
	}
	prefix := strings.TrimSuffix(gotASCII, expectedSuffix)
	if len(prefix) != 2048 {
		t.Fatalf("前缀字节数 got %d, want 2048", len(prefix))
	}
	if prefix != strings.Repeat("b", 2048) {
		t.Fatalf("前缀内容不匹配")
	}

	// 含中文的长串截后 utf8.ValidString 为真
	// 构造让 2048 边界恰好落在一个 3 字节中文字符中间的串
	// 2047 个 'x' + "中" (3字节，从 2047 到 2050 字节，2048 是其中间字节) + 剩余内容
	chineseStr := strings.Repeat("x", 2047) + "测试中文字符截断安全边界" + strings.Repeat("y", 1000)
	gotChinese := clipSQL(chineseStr)
	if !utf8.ValidString(gotChinese) {
		t.Fatalf("含中文长串截断后不是合法的 UTF-8 字符串: %q", gotChinese)
	}
	expectedChineseSuffix := fmt.Sprintf("…(共 %d 字节)", len(chineseStr))
	if !strings.HasSuffix(gotChinese, expectedChineseSuffix) {
		t.Fatalf("含中文长串后缀不匹配: %q", gotChinese)
	}
	chinesePrefix := strings.TrimSuffix(gotChinese, expectedChineseSuffix)
	if len(chinesePrefix) != 2047 {
		t.Fatalf("中文截断应退回到字符起始边界(2047)，got %d", len(chinesePrefix))
	}
}

func TestTraceClipsSlowSQL(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)
	ctx := plog.With(context.Background(), logger)

	l := newGormLogger(logger, 0)
	longSQL := strings.Repeat("SELECT ", 1000) // 7000 字节，> 5000 字节
	l.Trace(ctx, time.Now().Add(-time.Second), func() (string, int64) {
		return longSQL, 1
	}, nil)

	entries := logs.FilterMessage("slow sql").All()
	if len(entries) != 1 {
		t.Fatalf("期望 1 条 slow sql 日志，得到 %d 条", len(entries))
	}

	entry := entries[0]
	sqlVal, ok := entry.ContextMap()["sql"].(string)
	if !ok {
		t.Fatalf("日志缺少 string 类型的 sql 字段")
	}

	if len(sqlVal) > 2048+40 {
		t.Fatalf("sql 字段长度 %d 超过了 2048+40", len(sqlVal))
	}

	expectedSuffix := fmt.Sprintf("…(共 %d 字节)", len(longSQL))
	if !strings.HasSuffix(sqlVal, expectedSuffix) {
		t.Fatalf("慢查询截断后未包含正确后缀: got %q, want suffix %q", sqlVal, expectedSuffix)
	}
}
