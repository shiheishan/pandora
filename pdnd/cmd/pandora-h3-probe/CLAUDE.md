# pdnd/cmd/pandora-h3-probe/
> L2 | 父级: /pdnd/CLAUDE.md

独立进程的 REALITY-over-HTTP/3 互操作客户端。刻意与 kernel 的进程内 H3 测试分开：它只依赖 internal/reality 与 internal/realityquic/http3 的线协议，不经 NativeCore 运行时，因此能作为日后桌面/移动客户端替换时的验收缝。linux-release job 与主二进制一起双架构构建。它证明的是"我方客户端 ↔ 我方服务端"，不构成 REALITY+XHTTP+H3 的第三方独立验证，能力矩阵的 external-reality-xhttp-h3-unverified 边界不因它解除。

成员清单
main.go: 探针入口。flag -addr/-server-name/-public-key/-short-id/-path/-payload/-timeout，REALITY 握手后经 HTTP/3 发 XHTTP 请求；给 -uuid 时在载荷前加 VLESS TCP 头（-dest-host/-dest-port），否则为裸 XHTTP 回显；打印 proto/status/bytes/body 供断言
*_test.go: probe_test.go 覆盖 VLESS 头编码，以及在测试进程内起 ServeH3Reality、把探针编译成独立二进制再运行的往返测试（编译 3 分钟上限与运行 20s 上限分开计时）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
