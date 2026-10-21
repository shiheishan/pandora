package handler

import "time"

// myBalanceResponse 对应 GET /v1/me/balance 的 JSON 响应。
type myBalanceResponse struct {
	Balance int64              `json:"balance"`
	History []balanceEntryView `json:"history"`
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
	Amount int64  `json:"amount"`
	Reason string `json:"reason"`
}

// adjustBalanceResponse 对应 POST /v1/users/:id/balance 成功时的 JSON 响应。
type adjustBalanceResponse struct {
	Balance int64 `json:"balance"`
}

// userBalanceEntriesResponse 对应 GET /v1/users/:id/balance-entries 的 JSON 响应。
type userBalanceEntriesResponse struct {
	Entries []adminBalanceEntryView `json:"entries"`
	Total   int64                   `json:"total"`
}

// adminBalanceEntryView 是后台余额流水的一条；金额是整数分，delta 入账为正、出账为负。
type adminBalanceEntryView struct {
	ID           string    `json:"id"`
	Kind         string    `json:"kind"`
	Delta        int64     `json:"delta"`
	BalanceAfter int64     `json:"balance_after"`
	Memo         string    `json:"memo"`
	OrderID      *string   `json:"order_id"`
	At           time.Time `json:"at"`
}
