package adminops

import "time"

// 批量生成账号任务的进程内唤醒。
//
// worker 原先每 3 秒问一次库「有没有活」，静默时 99.9% 的询问是空的（w10quiet 的 pgss：
// 960 秒 320 次）。登记任务的入口（SubmitGenerateUsers）和 worker 在同一个 aegis-admin 进程里，
// 登记提交之后直接叫醒 worker，不必再靠勤快的轮询：任务的开工时延从最长 3 秒变成立刻。
//
// 仍保留一条慢轮询（UserGenerationPollEvery，aegis-admin 的循环节拍）兜底三件事：别的
// 实例登记的任务（多实例时唤醒不跨进程）、租约过期被丢下的任务、进程刚启动时库里已有的排队
// 任务。唤醒只是「去看一眼」的提示，丢了不会丢任务。

// UserGenerationPollEvery 是没有唤醒时 worker 回库看一眼的间隔。
const UserGenerationPollEvery = 15 * time.Second

// UserGenerationWake 返回唤醒通道：登记任务后有一个信号。容量为 1，多次登记合并成一次唤醒。
func (s *Service) UserGenerationWake() <-chan struct{} { return s.userGenWake }

// wakeUserGeneration 在登记事务提交之后调用：非阻塞，worker 正忙时信号留在通道里，做完就接着取。
func (s *Service) wakeUserGeneration() {
	select {
	case s.userGenWake <- struct{}{}:
	default:
	}
}
