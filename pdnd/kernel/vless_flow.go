package kernel

import (
	"errors"
	"fmt"
	"unicode/utf8"
)

// VLESS Addons 与流控（flow）。
//
// 协议里 UUID 之后跟着一段变长 addons，承载流控名等扩展字段。这段原来被
// 读出来直接丢弃——后果不是「不支持 Vision」，而是更坏的一种：客户端配了
// xtls-rprx-vision（当下最常见的配置），握手照样通过，随后它按 Vision 的帧
// 格式发数据（每帧带 padding 头），服务端把这些帧当裸字节转给目标网站。
// 连接建立、不报错、就是不通，日志里也看不出所以然。
//
// 所以第一件事是把这段解出来，认识的就按它的规矩走，不认识的当场拒绝并
// 说清楚是哪个值——让人去改配置，而不是留一个查不出来的故障。

// Addons 是 xray 的 protobuf 定义：
//
//	message Addons { string Flow = 1; bytes Seed = 2; }
//
// 只有两个字段，且都是 length-delimited，手写解析比引入 protobuf 运行时
// 划算得多——后者会把一整套反射和注册表拖进这个内核。
const (
	addonsFieldFlow = 1
	addonsFieldSeed = 2
)

// 流控取值。
//
// xtls-rprx-direct 与 xtls-rprx-splice 是 XTLS 早期方案，上游已经移除，
// 现在的客户端不会再发，服务端也不该假装支持。
const (
	FlowNone           = ""
	FlowVision         = "xtls-rprx-vision"
	FlowVisionUDP443   = "xtls-rprx-vision-udp443"
	flowLegacyDirect   = "xtls-rprx-direct"
	flowLegacySplice   = "xtls-rprx-splice"
	maxVLESSFlowLength = 64
)

// VLESSAddons 是解析后的 addons。
type VLESSAddons struct {
	Flow string
	Seed []byte
}

var errAddonsTruncated = errors.New("vless addons 数据截断")

// ParseVLESSAddons 解析 addons 字节串。
//
// 空输入是合法的：不带流控的客户端发的就是长度 0。
func ParseVLESSAddons(b []byte) (VLESSAddons, error) {
	var out VLESSAddons
	for i := 0; i < len(b); {
		tag, n := parseAddonsVarint(b[i:])
		if n == 0 {
			return out, errAddonsTruncated
		}
		i += n
		fieldNum := tag >> 3
		wireType := tag & 0x7
		if wireType != 2 {
			// Addons 里两个字段都是 length-delimited。出现别的线型
			// 说明这不是我们认识的 addons，继续猜下去只会解出垃圾。
			return out, fmt.Errorf("vless addons 字段 %d 线型 %d 不支持", fieldNum, wireType)
		}
		length, n := parseAddonsVarint(b[i:])
		if n == 0 {
			return out, errAddonsTruncated
		}
		i += n
		if length > uint64(len(b)-i) {
			return out, errAddonsTruncated
		}
		value := b[i : i+int(length)]
		i += int(length)
		switch fieldNum {
		case addonsFieldFlow:
			if len(value) > maxVLESSFlowLength {
				return out, fmt.Errorf("vless flow 过长（%d 字节）", len(value))
			}
			if !utf8.Valid(value) {
				return out, errors.New("vless flow 不是合法 UTF-8")
			}
			out.Flow = string(value)
		case addonsFieldSeed:
			out.Seed = append([]byte(nil), value...)
		default:
			// 未知字段跳过。protobuf 的前向兼容就靠这条，
			// 上游加字段时我们不该直接断连。
		}
	}
	return out, nil
}

// parseAddonsVarint 读一个 protobuf varint，返回值与消耗的字节数。
// 返回 n==0 表示数据不完整或 varint 超长。
func parseAddonsVarint(b []byte) (uint64, int) {
	var value uint64
	for i := 0; i < len(b); i++ {
		if i > 9 {
			return 0, 0 // varint 最多 10 字节，再长就是坏数据
		}
		value |= uint64(b[i]&0x7f) << (7 * i)
		if b[i]&0x80 == 0 {
			return value, i + 1
		}
	}
	return 0, 0
}

// EncodeVLESSAddons 按同一套规则编码，供测试与将来的出站方向使用。
func EncodeVLESSAddons(a VLESSAddons) []byte {
	var out []byte
	if a.Flow != "" {
		out = append(out, byte(addonsFieldFlow<<3|2))
		out = appendAddonsVarint(out, uint64(len(a.Flow)))
		out = append(out, a.Flow...)
	}
	if len(a.Seed) > 0 {
		out = append(out, byte(addonsFieldSeed<<3|2))
		out = appendAddonsVarint(out, uint64(len(a.Seed)))
		out = append(out, a.Seed...)
	}
	return out
}

func appendAddonsVarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// NegotiateVLESSFlow 判断这个 flow 该怎么处理。
//
// 返回 vision 表示后续数据要走 Vision 帧解析；错误表示这条连接不该继续。
// 明确拒绝比静默降级重要：降级的表现是「能连上但打不开网页」，
// 用户会怀疑节点、怀疑线路，唯独想不到是流控没生效。
func NegotiateVLESSFlow(flow string) (vision bool, err error) {
	switch flow {
	case FlowNone:
		return false, nil
	case FlowVision, FlowVisionUDP443:
		return true, nil
	case flowLegacyDirect, flowLegacySplice:
		return false, fmt.Errorf(
			"vless flow %q 是已废弃的 XTLS 早期方案，上游客户端已移除，请改用 %s",
			flow, FlowVision)
	default:
		return false, fmt.Errorf("vless flow %q 不支持（支持：空 / %s / %s）",
			flow, FlowVision, FlowVisionUDP443)
	}
}
