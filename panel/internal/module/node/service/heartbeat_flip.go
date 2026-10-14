package service

import (
	"time"
)

// beatFlipsOnline 报告这一拍心跳是否把服务器从「离线」拉回「在线」：之前从没心跳，或上一次心跳距这一拍已达 NodeStaleAfter。
func beatFlipsOnline(prev *time.Time, at time.Time) bool {
	if prev == nil {
		return true
	}
	return at.Sub(*prev) >= NodeStaleAfter
}
