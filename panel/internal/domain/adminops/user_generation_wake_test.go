package adminops

import (
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestUserGenerationWakeCoalescesAndNeverBlocks(t *testing.T) {
	s := NewService(nil)
	wake := s.UserGenerationWake()
	select {
	case <-wake:
		t.Fatal("wake signalled before any job was submitted")
	default:
	}
	// 连登记三次：合成一次唤醒，登记方不阻塞
	s.wakeUserGeneration()
	s.wakeUserGeneration()
	s.wakeUserGeneration()
	select {
	case <-wake:
	default:
		t.Fatal("no wake after a submission")
	}
	select {
	case <-wake:
		t.Fatal("wakes were not coalesced")
	default:
	}
	// 没有通道的零值 Service（测试里直接构造）也不会卡住或 panic
	(&Service{}).wakeUserGeneration()
}

// 登记任务成功之后必须叫醒 worker：删掉这一行，worker 只剩 15 秒一次的兜底轮询，测试仍全绿。
// 需要真库的行为（成功登记有信号、被拒绝的登记没有）由 PG18 的 bulk_users_generate 用例钉住；
// 这里在不连库时钉住调用的位置——在登记事务成功返回之后、返回任务之前，失败分支不叫醒。
func TestSubmitGenerateUsersWakesWorkerAfterCommit(t *testing.T) {
	src := sourcetest.Load(t, ".").Decl("Service.SubmitGenerateUsers")
	if strings.Count(src, "s.wakeUserGeneration()") != 1 {
		t.Fatal("SubmitGenerateUsers must wake the worker exactly once")
	}
	tx := strings.Index(src, "s.pool.InTx(")
	failed := -1
	if tx >= 0 {
		if at := strings.Index(src[tx:], "if err != nil {\n\t\treturn nil, err\n\t}"); at >= 0 {
			failed = tx + at
		}
	}
	wake := strings.Index(src, "s.wakeUserGeneration()")
	ret := strings.LastIndex(src, "return job, nil")
	if tx < 0 || failed < 0 || !(tx < failed && failed < wake && wake < ret) {
		t.Fatal("the wake must come after the registration transaction's error check and before returning the job")
	}
}
