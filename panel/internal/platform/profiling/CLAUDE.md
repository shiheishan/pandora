# panel/internal/platform/profiling/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

pprof 诊断端口：压测与排障时给三个网关各开一个独立的回环端口，暴露 net/http/pprof
  - 不挂在网关路由上：pprof 能导出整个堆（含解密后的密钥与令牌）、能让进程连续采样几十秒，不该经过 nginx、不该和用户请求共用限流与超时，也不能因为一条路由写错就上了公网
  - 两道闸：地址合法性（只收回环 IP 字面量）在 platform/config 的 Load 里判，非法即网关拒绝启动；这里只对内核实际绑定的地址再验一次回环，挡住绕过 config 的调用
  - 自建 ServeMux，不经 DefaultServeMux；但引入 net/http/pprof 会在 init 里往 DefaultServeMux 注册，所以 platform/server 拒绝 nil Handler，两边一起保证 pprof 只出现在这个端口上
  - 不设 WriteTimeout（CPU profile 与 trace 要占住整个采样窗口），停机只给 1 秒宽限即强关，诊断请求不拖慢网关退出。

成员清单
profiling.go: Start（空地址返回 nil 即关闭；监听失败或非回环绑定返回错误，网关据此拒绝启动）、Server 的 Addr 与 Close（nil 上为空操作，调用方无条件 defer）、requireLoopback、只挂五个 pprof 处理器的 newMux
*_test.go: 空地址不监听、非回环绑定被拒（直接验 requireLoopback，不真绑全部网卡）、回环端口取得到 /debug/pprof/ 与 heap 且别的路径 404、Close 后端口释放、诊断 mux 不是 DefaultServeMux

法则: 成员完整·一行一文件·父级链接·技术词前置
