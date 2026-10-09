package adminops

import "testing"

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
