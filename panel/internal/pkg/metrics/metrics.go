// Package metrics 面板的 Prometheus 指标：独立 registry（不用全局 DefaultRegisterer，测试可重复建）、
// HTTP 指标中间件、只挂 /metrics 的 HTTP 服务。指标名前缀 pandora_。
package metrics

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace 是面板全部自有指标的前缀。
const Namespace = "pandora"

// NewRegistry 返回独立 registry，并注册 Go 与进程采集器。
func NewRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return reg
}

// NewServer 返回只暴露 GET /metrics 的 HTTP 服务（不监听，由调用方 ListenAndServe / Shutdown）。
// 地址只能是回环或私网，由 config 校验；某个采集器出错时其余指标照常输出。
func NewServer(addr string, g prometheus.Gatherer) *http.Server {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", promhttp.HandlerFor(g, promhttp.HandlerOpts{
		ErrorHandling: promhttp.ContinueOnError,
	}))
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
}
