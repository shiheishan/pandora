package handler

import "time"

// myBalanceResponse 对应 GET /v1/me/balance 的 JSON 响应。
type myBalanceResponse struct {
	Balance  int64              `json:"balance"`
	Currency string             `json:"currency"`
	History  []balanceEntryView `json:"history"`
}

// balanceEntryView 是余额明细中的一条。
type balanceEntryView struct {
	Kind  string    `json:"kind"`
	Delta int64     `json:"delta"`
	Memo  string    `json:"memo"`
	At    time.Time `json:"at"`
}

// adjustBalanceRequest 对应 POST /v1/users/:id/balance 的请求体。
type adjustBalanceRequest struct {
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
	Reason   string `json:"reason"`
}

// adjustBalanceResponse 对应 POST /v1/users/:id/balance 成功时的 JSON 响应。
type adjustBalanceResponse struct {
	Balance int64 `json:"balance"`
}
