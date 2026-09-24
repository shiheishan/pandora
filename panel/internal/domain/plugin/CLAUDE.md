# panel/internal/domain/plugin/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

插件钩子：面板把业务事件以 HMAC 签名的 HTTP 请求推给插件自己的服务，插件跑在进程外——写得差的插件拖不垮面板，被攻破的插件拿不到数据库与主密钥。事件入队与业务写在同一个事务里（Emit 接调用方的 tx），投递由扫描器异步做，指数退避重试、4xx 不重试；地址在保存与每次发送前都做 SSRF 校验，不跟随跳转。

成员清单
hooks.go: 事件白名单 Events（EventInfo，小写 name / desc）、钩子列表带近 7 天 sent_count_7d、Service（钩子增删查、Dispatch / StartScanner 投递、Deliveries 投递记录、TestHook 同步测试）；每次尝试经 timedPost 量往返耗时，落 plugin_hook_deliveries.last_duration_ms（00081），没发出去的尝试记 NULL
emit.go: 各业务事件的发射薄封装（订单、订阅、注册、工单、礼品卡、流量），只拼载荷后交给 Emit
*_test.go: deliver_test.go 用 httptest 复算签名、验超时、生产模式挡内网与耗时只记发出去的请求

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
