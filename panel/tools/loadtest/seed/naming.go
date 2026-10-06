// [INPUT]: 依赖 crypto/rand、encoding/hex 与 net/netip，只用标准库
// [OUTPUT]: 包内提供 namespace（newNamespace / Email / NodeName / ServerName / NodeHost / PoolCode / PlanCode）、userIP、serverIP、newRunID、newUserPassword 与识别造数数据的 loadtestEmailDomain / loadtestNodePrefix
// [POS]: tools/loadtest/seed 的命名与地址分配：全部虚构数据的名字从这里出，users.go、nodes.go、retire.go 共用；纯函数，单测钉住格式与边界
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package seed

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math/big"
	"net/netip"
)

// ---------------------------------------------------------------------------
// 造数数据的识别标记
// ---------------------------------------------------------------------------
//
// 造出来的东西必须和库里别的数据（冒烟、e2e、真实运营）一眼分得开，
// 退役上一批时也只按这两个标记圈人，绝不按时间或通配去碰别人的行：
//   - 用户邮箱一律落在保留顶级域 .invalid 下（RFC 2606，永远不会被解析）
//   - 节点、服务器、池、套餐的名字与代码一律以 loadtest- 开头

const (
	loadtestEmailDomain = "loadtest.invalid"
	loadtestNodePrefix  = "loadtest-"
)

// namespace 是一次 seed 的命名空间：label 是档位（5k / ci …），run 是本次随机串。
// 同一库里重复 seed 时，新旧两批靠 run 区分，唯一约束不会互撞。
type namespace struct {
	Label string
	Run   string
}

func newNamespace(label, run string) namespace { return namespace{Label: label, Run: run} }

func (n namespace) base() string { return n.Label + "-" + n.Run }

// Email 是第 i 个用户（从 0 起）的邮箱。
func (n namespace) Email(i int) string {
	return fmt.Sprintf("lt-%s-u%06d@%s", n.base(), i+1, loadtestEmailDomain)
}

// NodeName 是第 i 个节点（从 0 起）的名字，也是接入令牌绑定的节点名。
func (n namespace) NodeName(i int) string {
	return fmt.Sprintf("%s%s-n%04d", loadtestNodePrefix, n.base(), i+1)
}

// ServerName 是第 j 台服务器（从 0 起）的名字。
func (n namespace) ServerName(j int) string {
	return fmt.Sprintf("%s%s-s%04d", loadtestNodePrefix, n.base(), j+1)
}

// NodeHost 是第 i 个节点对用户宣告的接入主机名（保留域，永远解析不到）。
func (n namespace) NodeHost(i int) string {
	return fmt.Sprintf("n%04d.%s.%s", i+1, n.base(), loadtestEmailDomain)
}

// PoolCode 与 PlanCode 同形：小写字母、数字与连字符，满足后台 2–64 位的代码规则。
func (n namespace) PoolCode() string { return loadtestNodePrefix + n.base() }
func (n namespace) PlanCode() string { return loadtestNodePrefix + n.base() }

// ---------------------------------------------------------------------------
// 地址分配
// ---------------------------------------------------------------------------

// userNet 是 RFC 2544 的基准测试网段 198.18.0.0/15，共 512 个 /24。
// 用户按序号轮流落进这 512 个 /24（第 i 个用户在第 i%512 个 /24 的 .(i/512+1)）：
// 面板门户有按 /24 聚合的限流（middleware.ByIPPrefix，登录默认每 /24 十分钟 60 次），
// 顺序分配会让 254 个模拟用户挤在同一个 /24 里撞上真实用户撞不上的网段限流，
// 也会让风控的网段聚类失真。15k 用户时每个 /24 约 30 人。
// 同一序号每次都同一个 IP，两档压测之间能对上号。
var userNet = netip.MustParsePrefix("198.18.0.0/15")

const userNets = 512 // userNet 里 /24 的个数

// maxUsers 是 userNet 能分配的用户数上限：每个 /24 用 .1–.254。
const maxUsers = userNets * 254

func userIP(i int) (string, error) {
	if i < 0 || i >= maxUsers {
		return "", fmt.Errorf("user index %d outside 0..%d", i, maxUsers-1)
	}
	return offsetAddr(userNet.Addr(), uint32(i%userNets)<<8|uint32(i/userNets+1)).String(), nil
}

// serverIP 给第 j 台服务器一个 TEST-NET-3（203.0.113.0/24）里的地址，循环使用 .1–.254。
// 面板不要求服务器地址唯一；放在与用户不同的网段，日志里不会把服务器和用户混成一个来源。
func serverIP(j int) string {
	return offsetAddr(netip.MustParseAddr("203.0.113.0"), uint32(j%254+1)).String()
}

func offsetAddr(base netip.Addr, off uint32) netip.Addr {
	b := base.As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v += off
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// ---------------------------------------------------------------------------
// 随机量：本次 run 的命名空间与全体用户共用的门户口令
// ---------------------------------------------------------------------------

// newRunID 是 6 位小写十六进制，进名字与代码（满足代码字符集）。
func newRunID() (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newUserPassword 每次 seed 现生成，只写进 manifest，从不出现在仓库里。
// 大小写字母与数字都有，满足口令策略（真实用户改密时的那套规则）。
func newUserPassword() (string, error) {
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789"
	out := []byte("Lt-")
	for i := 0; i < 20; i++ {
		v, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out = append(out, alphabet[v.Int64()])
	}
	return string(append(out, 'a', '7', 'Q')), nil
}
