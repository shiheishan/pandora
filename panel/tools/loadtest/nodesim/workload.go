// [INPUT]: 依赖 tools/loadtest/ltkit 的 Manifest（用户的虚构来源 IP），依赖 domain/nodefabric 的 Metrics（签名心跳的指标口径），math/rand/v2
// [OUTPUT]: 对外提供 包内 workload（newWorkload、onlineOn、trafficFor、aliveFor）、hostState（metrics、status）
// [POS]: tools/loadtest/nodesim 的虚构负载：代替 pdnd 内核的 GetTraffic / OnlineIPs 与 /proc 采样，只造面板看得见的那部分（流量增量、在线 IP、主机指标），全部是虚构值
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodesim

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"time"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

//------------------------------------------------------------------------------
// 在线用户与流量
//------------------------------------------------------------------------------
//
// 真实 pdnd 上报的是「内核里这段时间有流量的用户」。模拟器没有内核，按一条
// 固定规则决定谁在线、在哪个节点上：
//
//   - 用户 uid（面板下发列表里的 id，也就是订阅的 node_uid）经一次混洗哈希，
//     低位对模拟节点总数取模决定它「连在」哪个节点上，高位决定它在不在线；
//   - 在线比例 ratio 是全体用户里同时在线的比例，每个在线用户只落在一个
//     节点上——和真实用户一次只连一个节点相符，push 的总条数 ≈ 在线人数。
//
// 只有出现在该节点用户列表里的 uid 才可能被选中：pdnd 内核里没有的用户
// 不会有流量，模拟器也一样。

type workload struct {
	ratio      float64
	trafficMiB float64
	total      int
	ips        []string
}

func newWorkload(m *ltkit.Manifest, total int, ratio, trafficMiB float64) *workload {
	w := &workload{ratio: ratio, trafficMiB: trafficMiB, total: max(total, 1)}
	for _, u := range m.Users {
		if u.RealIP != "" {
			w.ips = append(w.ips, u.RealIP)
		}
	}
	return w
}

// mix 是 splitmix64 的终结函数：连续的 uid 也能打散到各个节点上。
func mix(x uint64) uint64 {
	x += 0x9e3779b97f4a7c15
	x = (x ^ (x >> 30)) * 0xbf58476d1ce4e5b9
	x = (x ^ (x >> 27)) * 0x94d049bb133111eb
	return x ^ (x >> 31)
}

// onlineOn 判断 uid 此刻是否在线且连在第 index 个模拟节点上。
func (w *workload) onlineOn(uid int64, index int) bool {
	h := mix(uint64(uid))
	if int(h%uint64(w.total)) != index {
		return false
	}
	return float64((h>>32)%10000) < w.ratio*10000
}

// trafficFor 给本节点的在线用户造一段流量增量，格式即 UniProxy push 的
// {"<uid>": [upload, download]}。下行均值 trafficMiB，上行取其八分之一，
// 各乘 [0.5, 1.5) 的随机系数。返回总字节数供主机网卡计数累加。
func (w *workload) trafficFor(ids []int64, index int, rng *rand.Rand) (map[string][2]int64, int64) {
	out := map[string][2]int64{}
	var sum int64
	mean := w.trafficMiB * (1 << 20)
	for _, id := range ids {
		if !w.onlineOn(id, index) {
			continue
		}
		down := int64(mean * (0.5 + rng.Float64()))
		up := int64(mean / 8 * (0.5 + rng.Float64()))
		if up+down <= 0 {
			continue
		}
		out[strconv.FormatInt(id, 10)] = [2]int64{up, down}
		sum += up + down
	}
	return out, sum
}

// aliveFor 给本节点的在线用户各配一到两个来源 IP，格式即 UniProxy alive 的
// {"<uid>": ["ip", ...]}。IP 取自清单里用户的虚构地址；清单没有时退到
// 198.18.0.0/15（RFC 2544 基准测试网段，不会撞上真实地址）。
func (w *workload) aliveFor(ids []int64, index int) map[string][]string {
	out := map[string][]string{}
	for _, id := range ids {
		if !w.onlineOn(id, index) {
			continue
		}
		h := mix(uint64(id) ^ 0x5bd1e995)
		ips := []string{w.ip(h)}
		if h%5 == 0 { // 约两成用户同时有两个设备在线
			ips = append(ips, w.ip(h>>17))
		}
		out[strconv.FormatInt(id, 10)] = ips
	}
	return out
}

func (w *workload) ip(h uint64) string {
	if len(w.ips) > 0 {
		return w.ips[h%uint64(len(w.ips))]
	}
	return fmt.Sprintf("198.%d.%d.%d", 18+h%2, (h>>8)%256, 1+(h>>16)%254)
}

//------------------------------------------------------------------------------
// 主机指标
//------------------------------------------------------------------------------

// hostState 代替 pdnd 的 /proc 采样：一台 2 核 2 GiB 40 GiB 的虚构小机器，
// 负载在一个区间里抖，网卡计数随上报的流量单调增长（面板按相邻两点差分算速率）。
type hostState struct {
	bootAt   time.Time
	rx, tx   int64
	memUsed  int
	diskUsed int
}

const (
	hostCPUCores  = 2
	hostMemoryMB  = 2048
	hostDiskGB    = 40
	hostUptimeMin = 3 * 24 * time.Hour
)

func newHostState(rng *rand.Rand) *hostState {
	return &hostState{
		bootAt:   time.Now().Add(-hostUptimeMin - time.Duration(rng.Int64N(int64(48*time.Hour)))),
		memUsed:  500 + rng.IntN(500),
		diskUsed: 6 + rng.IntN(6),
	}
}

func (h *hostState) addTraffic(bytes int64) {
	// 代理节点收进来多少就发出去多少，两张卡各记一遍
	h.rx += bytes
	h.tx += bytes
}

// metrics 是签名心跳的 metrics 对象（面板 nodefabric.Metrics 的整数口径），
// 值都落在面板 validate 的范围内。
func (h *hostState) metrics(online int, rng *rand.Rand) *nodefabric.Metrics {
	load := 5 + rng.IntN(60)
	return &nodefabric.Metrics{
		CPUBasisPoints: 300 + rng.IntN(2200),
		MemUsedMB:      h.memUsed + rng.IntN(64),
		MemTotalMB:     hostMemoryMB,
		DiskUsedGB:     h.diskUsed,
		DiskTotalGB:    hostDiskGB,
		Load1CBP:       load,
		Load5CBP:       load * 9 / 10,
		Load15CBP:      load * 8 / 10,
		NetRxBytes:     h.rx,
		NetTxBytes:     h.tx,
		TCPConns:       12 + online*3,
		UptimeSec:      int64(time.Since(h.bootAt).Seconds()),
	}
}

// compatStatus 是兼容通道 /status 的字节口径（pdnd status.go 的 RuntimeStatus）。
type compatStatus struct {
	CPU  float64      `json:"cpu"`
	Mem  resourcePair `json:"mem"`
	Swap resourcePair `json:"swap"`
	Disk resourcePair `json:"disk"`
}

type resourcePair struct {
	Used  uint64 `json:"used"`
	Total uint64 `json:"total"`
}

func (h *hostState) status(rng *rand.Rand) compatStatus {
	return compatStatus{
		CPU:  3 + rng.Float64()*22,
		Mem:  resourcePair{Used: uint64(h.memUsed) << 20, Total: hostMemoryMB << 20},
		Disk: resourcePair{Used: uint64(h.diskUsed) << 30, Total: hostDiskGB << 30},
	}
}
