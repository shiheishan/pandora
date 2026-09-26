// [INPUT]: 依赖 core 的 User，依赖 vless_flow.go 的 addons 解码
// [OUTPUT]: 包内提供 vlessDestination（各协议入站共用的目的地址）与 readVLESSRequest
// [POS]: kernel 的 VLESS 请求头解析：从 vless.go 拆出。读版本、UUID（经 lookup 定位用户）、addons、命令与目的地址；vlessDestination 也被 Trojan、Shadowsocks（含 2022）、socks / http 与 naive 入站复用
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package kernel

import (
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"

	"github.com/google/uuid"

	"github.com/aegispanel/nodeagent/core"
)

type vlessDestination struct {
	Command byte
	Host    string
	Domain  string
	IP      netip.Addr
	Port    uint16
	// Flow 来自请求头里的 addons。空表示不启用流控。
	Flow string
	// Vision 表示这条连接后续要按 XTLS Vision 的帧格式收发。
	Vision bool
	// RawUUID 是握手里那 16 个原始字节。Vision 的首帧前缀要跟它逐字节
	// 比对，从字符串再解析回去只是徒增一处可能失败的转换。
	RawUUID [16]byte
}

func readVLESSRequest(conn net.Conn, lookup func(string) (core.User, bool)) (core.User, vlessDestination, error) {
	var out vlessDestination
	var version [1]byte
	if _, err := io.ReadFull(conn, version[:]); err != nil || version[0] != vlessVersion {
		return core.User{}, out, fmt.Errorf("vless version 无效")
	}
	var id [16]byte
	if _, err := io.ReadFull(conn, id[:]); err != nil {
		return core.User{}, out, fmt.Errorf("vless uuid 缺失")
	}
	user, ok := lookup(uuid.UUID(id).String())
	if !ok {
		return core.User{}, out, fmt.Errorf("vless 用户未授权")
	}
	out.RawUUID = id
	var addonLen [1]byte
	if _, err := io.ReadFull(conn, addonLen[:]); err != nil {
		return core.User{}, out, err
	}
	if addonLen[0] > 64 {
		return core.User{}, out, fmt.Errorf("vless addon 过长")
	}
	addon := make([]byte, addonLen[0])
	if _, err := io.ReadFull(conn, addon); err != nil {
		return core.User{}, out, err
	}
	// 这段以前读完就扔。客户端配了 xtls-rprx-vision 时握手照样通过，
	// 之后它按 Vision 帧格式发数据，服务端把带 padding 头的帧当裸字节
	// 转给目标——连得上、不报错、就是不通。宁可在这里明确拒绝。
	addons, err := ParseVLESSAddons(addon)
	if err != nil {
		return core.User{}, out, err
	}
	out.Flow = addons.Flow
	vision, err := NegotiateVLESSFlow(addons.Flow)
	if err != nil {
		return core.User{}, out, err
	}
	out.Vision = vision
	var command [1]byte
	if _, err := io.ReadFull(conn, command[:]); err != nil {
		return core.User{}, out, err
	}
	if command[0] != vlessTCP && command[0] != vlessUDP && command[0] != vlessMux {
		return core.User{}, out, fmt.Errorf("vless 原生切片暂不接受 command=%d", command[0])
	}
	out.Command = command[0]
	// XUDP/mux carries its per-stream destination in the mux frame; the
	// request header therefore ends immediately after command=3.
	if out.Command == vlessMux {
		return user, out, nil
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return core.User{}, out, err
	}
	out.Port = binary.BigEndian.Uint16(port[:])
	if out.Port == 0 {
		return core.User{}, out, fmt.Errorf("vless 目标端口无效")
	}
	var addrType [1]byte
	if _, err := io.ReadFull(conn, addrType[:]); err != nil {
		return core.User{}, out, err
	}
	switch addrType[0] {
	case 1:
		buf := make([]byte, 4)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return core.User{}, out, err
		}
		out.IP = netip.AddrFrom4([4]byte{buf[0], buf[1], buf[2], buf[3]})
		out.Host = out.IP.String()
	case 2:
		var length [1]byte
		if _, err := io.ReadFull(conn, length[:]); err != nil {
			return core.User{}, out, err
		}
		if length[0] == 0 || length[0] > 253 {
			return core.User{}, out, fmt.Errorf("vless domain 长度无效")
		}
		buf := make([]byte, length[0])
		if _, err := io.ReadFull(conn, buf); err != nil {
			return core.User{}, out, err
		}
		out.Domain, out.Host = string(buf), string(buf)
	case 3:
		buf := make([]byte, 16)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return core.User{}, out, err
		}
		var ip [16]byte
		copy(ip[:], buf)
		out.IP = netip.AddrFrom16(ip)
		out.Host = out.IP.String()
	default:
		return core.User{}, out, fmt.Errorf("vless 地址类型无效")
	}
	return user, out, nil
}
