package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

// 向导绑池校验池时要锁住池行到事务结束：FOR KEY SHARE 只挡删池，挡不住并发停用，
// 套餐会绑到一个刚停用的池上；FOR SHARE 两者都挡（与 nodefabric.validatePool 同一取舍）。
// 加锁前按 id 排序，与 SetPlanPools 的加锁顺序一致，避免两个请求反向互等。
func TestBindPoolsTxHoldsShareLockInIDOrder(t *testing.T) {
	body := sourcetest.Load(t, ".").Decl("bindPoolsTx")
	if !strings.Contains(body, "FOR SHARE") || strings.Contains(body, "FOR KEY SHARE") {
		t.Fatal("bindPoolsTx 校验池时必须 FOR SHARE 锁住池行")
	}
	sortAt := strings.Index(body, "slices.Sort(unique)")
	lockAt := strings.Index(body, "FOR SHARE")
	if sortAt < 0 || lockAt < sortAt {
		t.Fatal("bindPoolsTx 必须先按 id 排序再逐个加锁")
	}
}
