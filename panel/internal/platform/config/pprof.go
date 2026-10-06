// [INPUT]: 依赖 config.go 的 Domain 常量，依赖 deployment.go 的 trimmedEnv 读环境变量，依赖 net/netip 解析地址
// [OUTPUT]: 对外提供 PprofAddrEnv（各网关的 pprof 地址变量名）；包内 loadPprofAddrs 供 Load 调用
// [POS]: platform/config 的诊断端口配置：三个网关各一个可缺省的 pprof 监听地址，只许回环 IP 字面量，非法即让 Load 拒绝启动；监听本身在 platform/profiling
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
)

//------------------------------------------------------------------------------
// pprof 诊断端口：默认关闭，打开时每个网关一个独立的回环端口
//------------------------------------------------------------------------------

// PprofAddrEnv 是各网关 pprof 监听地址的环境变量名。
//
// 为什么是每个网关一个变量：三个网关的 systemd 单元加载的是同一份 .env，
// 一个共用变量会让三个进程去抢同一个端口。与 AEGIS_PUBLIC_ADDR 这组业务端口
// 同一个命名习惯，压测时只开要看的那个网关。
var PprofAddrEnv = map[Domain]string{
	DomainPublic: "AEGIS_PUBLIC_PPROF_ADDR",
	DomainAdmin:  "AEGIS_ADMIN_PPROF_ADDR",
	DomainNode:   "AEGIS_NODE_PPROF_ADDR",
}

// loadPprofAddrs 读三个网关的 pprof 地址：未设置的网关不出现在结果里（即关闭）。
//
// 设置了就必须是「回环 IP 字面量:端口」，否则 Load 失败、网关拒绝启动——
// pprof 能拿到堆里的一切（含解密后的密钥与令牌），绝不能因为一次手误监听到公网。
// 主机名一律不收，localhost 也不收：它的解析结果取决于 /etc/hosts 与解析顺序，
// 校验时是回环不代表监听时还是。端口 0（随机端口）也不收，运维找不到它。
// gatewayAddrs 是四个业务端口，pprof 不许与它们或彼此撞车，撞了第二个进程会起不来。
func loadPprofAddrs(gatewayAddrs []string) (map[Domain]string, error) {
	taken := map[string]string{}
	for _, raw := range gatewayAddrs {
		taken[normalizeAddr(raw)] = "网关业务端口"
	}
	out := map[Domain]string{}
	for _, d := range []Domain{DomainPublic, DomainAdmin, DomainNode} {
		name := PprofAddrEnv[d]
		raw := trimmedEnv(name, "")
		if raw == "" {
			continue
		}
		addr, err := parseLoopbackAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("%s=%q 无效：%w", name, raw, err)
		}
		if owner, dup := taken[addr]; dup {
			return nil, fmt.Errorf("%s=%q 与%s重复，每个 pprof 端口必须独占", name, raw, owner)
		}
		taken[addr] = name
		out[d] = addr
	}
	return out, nil
}

// parseLoopbackAddr 校验并规范化「回环 IP:端口」，IPv6 写成 [::1]:6060。
func parseLoopbackAddr(raw string) (string, error) {
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", errors.New("必须写成 127.0.0.1:端口 或 [::1]:端口")
	}
	n, err := strconv.ParseUint(port, 10, 16)
	if err != nil || n == 0 {
		return "", errors.New("端口必须是 1–65535")
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return "", errors.New("主机必须是回环 IP 字面量（127.0.0.0/8 或 ::1），不接受主机名，localhost 也不行")
	}
	if ip.Zone() != "" || !ip.Unmap().IsLoopback() {
		return "", errors.New("只允许回环地址 127.0.0.0/8 或 ::1，pprof 不能对外监听")
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(n)).String(), nil
}

// normalizeAddr 把能解析的「IP:端口」规范化，便于判重；解析不了的原样返回。
func normalizeAddr(raw string) string {
	if ap, err := netip.ParseAddrPort(raw); err == nil {
		return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
	}
	return raw
}
