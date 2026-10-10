package nodefabric

import (
	"strings"
	"testing"
)

// 小时桶的 billed_bytes 已经非空，按天汇总直接相加，不再把有空桶的一整天记成未知。
func TestTrafficDailyRollupSumsBilledBytes(t *testing.T) {
	if strings.Contains(trafficDailyRollupSQL, "bool_and") {
		t.Fatal("daily rollup still treats a NULL billed hour as an unknown day")
	}
	if !strings.Contains(trafficDailyRollupSQL, "sum(h.billed_bytes)") {
		t.Fatal("daily rollup does not sum billed_bytes")
	}
}
