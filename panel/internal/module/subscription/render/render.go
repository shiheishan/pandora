// Package render 把一组可下发的线路渲染成客户端订阅（Clash Meta、Clash Premium、sing-box、URI）。
//
// 纯函数包：不碰数据库、不读配置、不依赖任何模块。输入的协议配置已是内核字段名
// （由 node 模块按下发给节点的同一张映射表翻译好），这里只管各格式的写法。
// P4 骨架：签名由主 Agent 定，函数体由 p4-render 块照旧实现逐字节移植。
package render

import "time"

// Format 是输出格式。
type Format string

// 输出格式。
const (
	FormatClash        Format = "clash"
	FormatClashPremium Format = "clash-premium"
	FormatSingbox      Format = "singbox"
	FormatURI          Format = "uri"
)

// Node 是一条可下发的线路。Type 是规范化后的协议名，Config 是内核字段名的协议配置（只读，渲染不改它）。
type Node struct {
	Name        string
	Type        string
	Host        string
	Port        int
	Config      map[string]any
	TrafficRate float64
}

// SingboxTemplate 是 sing-box 订阅附带模板的可配项，空字段用缺省值。
type SingboxTemplate struct {
	GeositeURLPrefix string
	GeoIPURLPrefix   string
}

// Options 是渲染的可配项（来自进程配置，启动时定一次）。
type Options struct {
	Singbox SingboxTemplate
}

// DetectFormat 按 User-Agent 与显式参数（flag / target）判断客户端要的格式；认不出的给 URI。
func DetectFormat(ua, explicit string) Format {
	panic("render: 未实现")
}

// UAFamily 返回审计与日志用的客户端家族，不含可识别信息。
func UAFamily(ua string) string {
	panic("render: 未实现")
}

// Render 把节点渲染成指定格式，返回内容、Content-Type 与实际写进去的节点数（连不上的组合会跳过）。
// proxyUUID 是这份订阅在节点上的身份；ua 用来挑 sing-box 的版本写法。
func Render(f Format, nodes []Node, proxyUUID, ua string, opt Options) ([]byte, string, int) {
	panic("render: 未实现")
}

// ExpiredNotice 是过期订阅里那条提示节点的名字（到期时刻按 loc 显示）。
func ExpiredNotice(periodEnd time.Time, loc *time.Location) string {
	panic("render: 未实现")
}

// RenderExpired 渲染过期订阅：只有一条写着 notice 的提示节点，返回内容与 Content-Type。
func RenderExpired(f Format, notice string) ([]byte, string) {
	panic("render: 未实现")
}

// ProfileName 是客户端里显示的订阅名（站点名、订阅标签、套餐名拼成）。
func ProfileName(site, label, planName string) string {
	panic("render: 未实现")
}

// ContentDisposition 是带订阅名的 Content-Disposition 头（RFC 6266 / 5987）。
func ContentDisposition(profileName string) string {
	panic("render: 未实现")
}
