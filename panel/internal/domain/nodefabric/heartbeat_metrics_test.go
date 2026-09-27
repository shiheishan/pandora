// [INPUT]: 依赖 heartbeat.go 的 Metrics.validate、platform/httpx 的错误码
// [OUTPUT]: 对外提供 TestHeartbeatMetricsRange
// [POS]: domain/nodefabric 的心跳探针范围单测：边界值放行，越界（cpu_bp、int4 列宽、负数）一律 400；落库与事务不回滚由 api/node 的 PG18 测试核对
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"errors"
	"math"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

func TestHeartbeatMetricsRange(t *testing.T) {
	valid := Metrics{CPUBasisPoints: 10000, MemUsedMB: math.MaxInt32, NetRxBytes: math.MaxInt64, UptimeSec: 1}
	if err := valid.validate(); err != nil {
		t.Fatalf("boundary metrics rejected: %v", err)
	}
	for name, m := range map[string]Metrics{
		"cpu_bp high":     {CPUBasisPoints: 10001},
		"cpu_bp negative": {CPUBasisPoints: -1},
		"int4 overflow":   {DiskTotalGB: math.MaxInt32 + 1},
		"load negative":   {Load15CBP: -1},
		"bigint negative": {UptimeSec: -1},
	} {
		err := m.validate()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeBadRequest {
			t.Errorf("%s: err=%v, want a 400", name, err)
		}
	}
}
