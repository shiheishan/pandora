package kernel

import (
	"errors"
	"sync"
	"time"
)

// 旧版 Shadowsocks AEAD 的 UDP 防重放：每个认证通过的 UDP 包都查 salt（SIP004 每包一个随机
// salt），见过就丢。不查的话，抓到一个合法包就能用伪造来源反复重放：节点每次都转发、把上游
// 回包发给伪造地址（反射），还占着受害用户的设备与会话名额。shadowsocks-libev 的 UDP 同样查。
//
// 与 TCP 同类型（saltBloom，按时间分代、误报 1e-6），但单独一个实例：UDP 每包一条，量级比
// TCP 每连接一条高几个数量级，共用会把 TCP 的保留期挤短。只记认证通过的 salt，没有口令的人
// 写不进这张表。
//
// 容量估算：位图每条约 3.6 字节（m/n = -ln(1e-6)/ln²2 ≈ 28.8 位）。一个忙节点 UDP 1 万包/秒，
// 一代（ssSaltGenPeriod，15 分钟）约 900 万条，精确保留一整代要 32MB，不划算。块上限取
// 1<<21 条（约 7.5MB），总容量封顶两块约 15MB、约 420 万条：1 万包/秒时保留约 7 分钟，
// 5 万包/秒时约 80 秒。重放探测一半在 1 分钟内（IMC'20），这个保留期挡住大部分。块从
// 1<<12 条（约 15KB）起按流量长，整整两代没有写入全部丢掉，空闲节点几乎不占内存。
// 每次 check 在一把全进程的锁里做约 20 次位图访问，5 万包/秒时不到一核的十分之一。
const (
	ssUDPSaltBloomMinCapacity = 1 << 12
	ssUDPSaltBloomMaxCapacity = 1 << 21
)

// sharedSSUDPSaltBloom 是全进程 SS UDP 共用的一张（各入站共用，理由同 sharedSSSaltBloom）。
var sharedSSUDPSaltBloom = sync.OnceValue(func() *saltBloom {
	return newSaltBloom(ssSaltGenPeriod, ssUDPSaltBloomMinCapacity, ssUDPSaltBloomMaxCapacity, ssSaltBloomFPRate)
})

var errSSUDPReplay = markConnError(connErrAuth, errors.New("shadowsocks UDP salt replayed"))

// acceptUDPSalt 报告认证通过的 UDP 包的 salt 是否没见过（没见过即记下）；见过就计一次重放丢包。
func (a *shadowsocksAdapter) acceptUDPSalt(salt []byte) bool {
	if a.udpSalts == nil || a.udpSalts.check(salt, time.Now()) {
		return true
	}
	a.udpDrops.replay.Add(1)
	return false
}
