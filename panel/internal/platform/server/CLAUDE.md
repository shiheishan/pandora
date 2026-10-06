# panel/internal/platform/server/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

三个网关共用的 HTTP server 生命周期：统一的读写与空闲超时、头部上限，信号 context 同时作每个连接的 BaseContext
  - 取消 context 先放掉 SSE 这类长连接处理器，再在 ShutdownTimeout 内优雅停机，超时强关
  - 有后台循环的网关（public、admin）自建信号 context 调 RunContext，以便停机后先 join 循环再关资源；没有循环的 node 直接用 Run
  - 拒绝 nil Handler：nil 会落到 DefaultServeMux，而网关链着 net/http/pprof（platform/profiling），那里挂着 /debug/pprof/。

成员清单
server.go: Options、Run（自带 SIGINT/SIGTERM）、RunContext 与可注入 listener 的 runContextWithListener
*_test.go: 取消 context 释放活动的长连接处理器并停机；nil Handler 在开服前被拒且 listener 被关闭

法则: 成员完整·一行一文件·父级链接·技术词前置
