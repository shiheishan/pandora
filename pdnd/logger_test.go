package main

import (
	"bytes"
	"log"
	"log/slog"
	"os"
	"strings"
	"testing"
)

// 包级 slog 与标准库 log 的输出都要走进程日志的结构化格式、受 log_level 约束：
// 否则 QUIC 缓冲偏小这类告警是「2026/10/09 ... WARN」格式，按 level= 采集会漏掉。
func TestInstallLoggerRoutesPackageLevelLogs(t *testing.T) {
	prev, prevFlags := slog.Default(), log.Flags()
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(os.Stderr)
		log.SetFlags(prevFlags)
	})
	var out bytes.Buffer
	installLogger("info", &out)
	slog.Warn("包级告警", "port", 20450)
	slog.Debug("低于 log_level 的包级日志")
	log.Print("标准库日志")
	got := out.String()
	if !strings.Contains(got, `level=WARN msg=包级告警 port=20450`) {
		t.Fatalf("包级 slog 没走结构化格式：%q", got)
	}
	if strings.Contains(got, "低于 log_level") {
		t.Fatalf("包级 slog 不受 log_level 约束：%q", got)
	}
	// 标准库 log（依赖库常用）按 INFO 转进同一个 handler。
	if !strings.Contains(got, `level=INFO msg=标准库日志`) {
		t.Fatalf("标准库 log 没转进进程日志：%q", got)
	}
	for _, line := range strings.Split(strings.TrimSpace(got), "\n") {
		if !strings.HasPrefix(line, "time=") {
			t.Fatalf("有一行不是结构化格式：%q", line)
		}
	}
}
