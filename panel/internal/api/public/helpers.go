package public

import (
	"context"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func timeoutCtx(r *http.Request, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), d)
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// decodeHex 解析十六进制签名。解析失败返回 nil，
// 交由恒定时间比较去拒绝 —— 提前 return 会引入可观测的时间差。
func decodeHex(s string) []byte {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil
	}
	return b
}
