package config

import (
	"strings"
	"testing"
)

func TestValidateMetricsAddr(t *testing.T) {
	ok := []string{
		"",
		"127.0.0.1:9100",
		"localhost:9100",
		"[::1]:9100",
		"10.0.0.5:9100",
		"172.16.3.4:9100",
		"192.168.1.10:9100",
		"[fd00::1]:9100",
	}
	for _, addr := range ok {
		if err := validateMetricsAddr("x", addr); err != nil {
			t.Errorf("应接受 %q: %v", addr, err)
		}
	}

	bad := []string{
		":9100",
		"0.0.0.0:9100",
		"[::]:9100",
		"8.8.8.8:9100",
		"172.32.0.1:9100",
		"panel.example.com:9100",
		"10.0.0.5",
		"10.0.0.5:0",
		"10.0.0.5:70000",
		"10.0.0.5:abc",
	}
	for _, addr := range bad {
		if err := validateMetricsAddr("x", addr); err == nil {
			t.Errorf("应拒绝 %q", addr)
		}
	}
}

func TestMetricsAddrFromEnv(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_OBSERVABILITY_METRICS_ADDR", "10.0.0.5:9100")
	t.Setenv("PANDORA_WORKER_METRICS_ADDR", "127.0.0.1:9101")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observability.MetricsAddr != "10.0.0.5:9100" {
		t.Fatalf("observability.metrics_addr: got %q", cfg.Observability.MetricsAddr)
	}
	if cfg.Worker.MetricsAddr != "127.0.0.1:9101" {
		t.Fatalf("worker.metrics_addr: got %q", cfg.Worker.MetricsAddr)
	}
}

func TestMetricsAddrDefaultEmpty(t *testing.T) {
	setMinimalValidEnv(t)
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Observability.MetricsAddr != "" {
		t.Fatalf("observability.metrics_addr 缺省应为空: %q", cfg.Observability.MetricsAddr)
	}
	if cfg.Worker.MetricsAddr != "" {
		t.Fatalf("worker.metrics_addr 缺省应为空: %q", cfg.Worker.MetricsAddr)
	}
}

func TestMetricsAddrPublicRejected(t *testing.T) {
	setMinimalValidEnv(t)
	t.Setenv("PANDORA_OBSERVABILITY_METRICS_ADDR", "0.0.0.0:9100")
	_, err := Load("")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "observability.metrics_addr") {
		t.Fatalf("unexpected: %v", err)
	}
}
