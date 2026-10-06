# panel/tools/loadtest/
> L2 | 父级: /panel/CLAUDE.md（tools/ 一行）

面板压测工具链（施工中，成员清单随子包落地补全）
  - main 包只经 `go run ./tools/loadtest` 使用，不进发布包、不被任何包 import
  - 四个子命令 seed / nodes / users / burst 之间只经 ltkit 的造数清单（Manifest）交换数据，计量统一走 ltkit.Recorder

成员清单
main.go: 子命令分发
ltkit/: 共享底座：manifest.go 造数清单（seed 写、其余读，含节点私钥，0600 落盘）；stats.go 对数分桶直方图计量（QPS、p50/p95/p99、错误码、自定义标签、按窗口时间线），写 <场景>.json 与 .txt 摘要
seed/: 造数（占位）
nodesim/: 模拟节点（占位）
userload/: 用户混合流量与 burst（占位）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
