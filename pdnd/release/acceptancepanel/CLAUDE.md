# release/acceptancepanel/
> L2 | 父级: pdnd/CLAUDE.md

runtime-acceptance.sh 的回环模拟面板。签名通道要 Ed25519，Python 标准库没有，所以用 Go 写；原像与校验按面板实现独立重写，不 import pdnd/panel，避免夹具与被测代码一起错时验收照样变绿。

成员清单
main.go: 可执行夹具。UniProxy 兼容通道（config / user / status 等）与签名通道（生成身份文件、按 V2 原像验每个节点请求、签发 effective release、收心跳与配置上报），每个事件立即落盘到 -state 指定的 JSON，脚本据此断言
main_test.go: 用 pdnd/panel 的 SignedClient 与夹具对打（验签发的 effective release、签名心跳带 metrics、配置上报），伪造签名被拒并计数，node-id 必须是规范 UUID

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
