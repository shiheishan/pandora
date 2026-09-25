# panel/frontend/dev/mock/
> L2 | 父级: /panel/frontend/dev/CLAUDE.md

假后端的模块层。dev/mock-api.ts 是外壳：持有账号、会话与 rat、幂等表，自己答外壳接口，其余请求按入口依次询问这里登记的模块；模块文件只依赖 types.ts 的上下文契约，不碰会话与令牌的实现（门户账号安全经 otherSessions / revokeSession 列出与吊销外壳会话），所以各页面的会话可以只改自己的文件。
写接口照后端中间件顺序用三个守卫：requirePermission（缺权限 404）→ requireReauth（403 reauth_required，不消耗幂等键）→ idempotent(scope, run)（与 Go 中间件一致，契约 R85：只有 2xx 原样重放，非 2xx 同键同请求重新执行，在途 409 conflict，换请求不论上次结果都回 409 idempotency_key_reuse；所以处理器不要在回 4xx 之前改状态）。形状、错误码、fields 键名一律照 api-contract.md（含修订 Rn），页面在假后端上走通就等于按契约走通。

成员清单
types.ts: 处理器契约——MockUser、AnonContext / MockContext（请求体、send / sendRaw / fail、路径参数、三个守卫、otherSessions / revokeSession）、MockResult 可带 raw（CSV 等非 JSON 响应，幂等重放与后端一致只回 Content-Type 与 Cache-Control）、MockModule { anonymous?, routes? }（键为「METHOD /v1/路径」，:name 匹配一段），matchPattern / findRoute
quick-login.ts: 快捷登录令牌表，签发在 portal/account.ts、消费在外壳的 POST v1/auth/quick-login，60 秒一次性，绑定签发会话、同会话重新生成作废旧令牌
admin/: 后台十个模块的假接口，与 src/admin/screens 一一对应；见 admin/CLAUDE.md
portal/: 门户十一个页面的假接口，与 src/portal/screens 一一对应；见 portal/CLAUDE.md

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
