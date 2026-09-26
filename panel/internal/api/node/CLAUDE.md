# panel/internal/api/node/
> L2 | 父级: /panel/internal/api/CLAUDE.md

Node 域网关：调用方是机器不是人，没有会话与用户令牌，身份靠每个请求的 Ed25519 签名证明（私钥只在节点本地）。两套协议并存：/api/v1/server/UniProxy/* 兼容 Xboard 节点端，用节点令牌鉴权（Bearer 优先、查询参数兜底）；/nodes/* 是 pdnd 的两阶段入网与签名配置下发。旧版 bootstrap 已关闭，只回固定拒绝。业务都在 domain/nodefabric，这一层只做验签、解码与响应；这里的文案只给节点看，httpx 的中文文案契约整包豁免。

成员清单
router.go: NewRouter 装 UniProxy、入网、签名节点三组路由；requireEnrollmentSignature / requireNodeSignature 两道验签中间件
handlers.go: 入网 begin/status/commit/abort、心跳、配置与签名密钥下发、配置回报；UniProxy 的 config/user/push/alive/status；authNode 只把凭据选择交给 uniProxyToken；ETag 协商
stream.go: 节点 SSE，推配置与用户变动，20 秒注释帧保活；是快车道不是唯一通路，失败即放弃，节点端有轮询兜底
*_test.go: uniProxyToken 的凭据优先级与 fail-closed、authNode 不自己读请求头（经 platform/sourcetest 取 AST）、旧 bootstrap 不碰节点服务；signed_e2e_pg18_test.go 由 run-pg18-gates.sh 跑签名链路端到端

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
