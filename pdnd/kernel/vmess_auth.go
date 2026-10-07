package kernel

import (
	"crypto/aes"
	"fmt"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
)

// VMess AEAD 认证的预计算（对应 Xray proxy/vmess/validator.go 的 AuthIDDecoderHolder）。
//
// 原先每条连接都把全部用户复制一遍，并对每个用户重做 KDF（多轮 HMAC-SHA256）
// 加 aes.NewCipher：1c1g 实测 5000 用户时每条连接耗 115ms CPU，每秒只能建 8.4 条。
// 现在加用户时就把每个用户的 AuthID 解密块算好，用户变更后重建一份只读快照，
// 握手只对快照里的块逐个解一次 16 字节（每个用户约几十纳秒），不分配、不加锁。

// newVMessUserEntry 校验并预计算一个用户。
func newVMessUserEntry(normalized string, u core.User, parsed uuid.UUID) (vmessUserEntry, error) {
	key := vmessCommandKey(parsed)
	block, err := aes.NewCipher(vmessKDF(key[:], "AES Auth ID Encryption")[:16])
	if err != nil {
		return vmessUserEntry{}, fmt.Errorf("vmess user %q auth key: %w", u.UUID, err)
	}
	return vmessUserEntry{uuid: normalized, user: u, key: key, authBlock: block}, nil
}

func (e vmessUserEntry) stored() vmessUser {
	return vmessUser{ID: e.user.ID, DeviceLimit: e.user.DeviceLimit, SpeedLimit: e.user.SpeedLimit, key: e.key, authBlock: e.authBlock}
}

// publishAuthCandidatesLocked 按当前用户表重建认证快照；调用方持有 a.mu 写锁。
func (a *vmessAdapter) publishAuthCandidatesLocked() {
	candidates := make([]vmessUserCandidate, 0, len(a.users))
	for id, u := range a.users {
		block := u.authBlock
		if block == nil {
			// 测试里直接塞进表的用户没有预计算，补算一次。
			var err error
			block, err = aes.NewCipher(vmessKDF(u.key[:], "AES Auth ID Encryption")[:16])
			if err != nil {
				continue
			}
		}
		candidates = append(candidates, vmessUserCandidate{
			user: core.User{ID: u.ID, UUID: id, DeviceLimit: u.DeviceLimit, SpeedLimit: u.SpeedLimit},
			key:  u.key, keyBlock: block,
		})
	}
	a.authCandidates.Store(&candidates)
}
