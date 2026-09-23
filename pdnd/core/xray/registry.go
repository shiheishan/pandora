package xray

// xray 的模块注册。
//
// xray 用 init() 副作用把各个模块登记到全局注册表里，没被 import 的模块
// 在运行时会以 "*proxyman.InboundConfig is not registered" 这类错误炸掉 ——
// 而编译期完全看不出来，因为构造配置只需要 protobuf 类型，不需要实现。
//
// 上游提供了 main/distro/all 一次性注册所有东西，这里没用它：那会把
// 全部协议、全部传输层都链进二进制（包括我们根本不会用的 DNS、路由、
// 各种出站协议），体积翻好几倍。这里只登记真正用得上的。
//
// 加新协议时记得回来加一行 —— 忘了的话表现是运行时报 not registered，
// 而不是编译错误。

import (
	// 应用层：调度、入站/出站管理、统计、策略
	_ "github.com/xtls/xray-core/app/dispatcher"
	_ "github.com/xtls/xray-core/app/log"
	_ "github.com/xtls/xray-core/app/policy"
	_ "github.com/xtls/xray-core/app/proxyman/inbound"
	_ "github.com/xtls/xray-core/app/proxyman/outbound"
	_ "github.com/xtls/xray-core/app/stats"

	// 入站协议
	_ "github.com/xtls/xray-core/proxy/trojan"
	_ "github.com/xtls/xray-core/proxy/vless/inbound"
	_ "github.com/xtls/xray-core/proxy/vmess/inbound"

	// 路由
	_ "github.com/xtls/xray-core/app/router"

	// 出站。direct/block 是内建的；其余几种用于分流到中转线路。
	_ "github.com/xtls/xray-core/proxy/blackhole"
	_ "github.com/xtls/xray-core/proxy/freedom"
	_ "github.com/xtls/xray-core/proxy/http"
	_ "github.com/xtls/xray-core/proxy/shadowsocks"
	_ "github.com/xtls/xray-core/proxy/socks"
	_ "github.com/xtls/xray-core/proxy/vless/outbound"
	_ "github.com/xtls/xray-core/proxy/vmess/outbound"

	// 传输层。splithttp 就是 XHTTP —— sing-box 没有它，
	// 这也是把 xray 作为第二内核引入的主要理由之一。
	_ "github.com/xtls/xray-core/transport/internet/grpc"
	_ "github.com/xtls/xray-core/transport/internet/httpupgrade"
	_ "github.com/xtls/xray-core/transport/internet/reality"
	_ "github.com/xtls/xray-core/transport/internet/splithttp"
	_ "github.com/xtls/xray-core/transport/internet/tcp"
	_ "github.com/xtls/xray-core/transport/internet/tls"
	_ "github.com/xtls/xray-core/transport/internet/websocket"
)
