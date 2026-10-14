// 节点端性能闸门的工具（scalepanel、loadgen、target）。独立 module：压测客户端要用
// xray-core、sing-quic、sing-anytls 当客户端，它们在 pdnd 里只许出现在测试里；放在这里，
// pdnd 的依赖边界不受影响。不许依赖 github.com/sagernet/sing-box（任何版本）。
// 版本对齐 pdnd/go.mod；go.sum 由主 Agent 跑 go mod tidy 生成。
module github.com/aegispanel/nodeagent/perfgate

go 1.26.9

require (
	github.com/aegispanel/nodeapi v0.0.0-00010101000000-000000000000
	github.com/anytls/sing-anytls v0.0.13
	github.com/google/uuid v1.6.0
	github.com/sagernet/sing v0.8.11
	github.com/sagernet/sing-quic v0.6.1
	github.com/xtls/xray-core v1.260327.0
	google.golang.org/grpc v1.83.2
	google.golang.org/protobuf v1.36.12
)

replace github.com/aegispanel/nodeapi => ../../../../../nodeapi
