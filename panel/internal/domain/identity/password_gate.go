package identity

import (
	"context"

	"github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// passwordBusyMessage 是口令哈希排不上名额时的统一回应文案（503）。
//
// 登录时不论账号是否存在都先排同一个队、回同一个错误，不给枚举留差别（IAM-006）。
const passwordBusyMessage = "当前请求较多，请稍后重试"

// acquirePasswordSlot 在开事务之前取口令哈希名额（见 crypto.AcquirePasswordSlot）。
//
// 先拿名额再开事务：排队的请求不占数据库连接；拿到名额的请求在事务里马上就能算，
// 不会拿着连接、锁着行去等别人的哈希算完。本包的口令计算一律经名额进行，
// 不调 crypto 的包级 HashPassword / VerifyPassword / DummyVerify（守卫
// password_gate_test.go），否则持名额的 goroutine 会再去排同一道闸。
func acquirePasswordSlot(ctx context.Context) (*crypto.PasswordSlot, error) {
	slot, err := crypto.AcquirePasswordSlot(ctx)
	if err != nil {
		return nil, httpx.New(httpx.CodeUnavailable, passwordBusyMessage).WithInternal(err)
	}
	return slot, nil
}
