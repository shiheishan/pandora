package service

import "slices"

// certTransitions 是证书状态的转换表（第 5.4 节）：签出 pending → active；暂停与恢复；凭据预检失败转
// blocked_credential，凭据重新校验通过后回 pending / active。repo 的状态更新带 WHERE status = 旧状态。
var certTransitions = map[string][]string{
	StatusPending:           {StatusActive, StatusPaused, StatusBlockedCredential},
	StatusActive:            {StatusPaused, StatusBlockedCredential},
	StatusPaused:            {StatusPending, StatusActive},
	StatusBlockedCredential: {StatusPending, StatusActive, StatusPaused},
}

// CanTransition 报告证书状态能否从 from 转到 to。
func CanTransition(from, to string) bool { return slices.Contains(certTransitions[from], to) }
