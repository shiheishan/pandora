# panel/frontend/dev/
> L2 | 父级: /panel/frontend/CLAUDE.md

只在 vite serve 时存在的开发期设施，永不进产物（vite.config.ts 只在 command === 'serve' 时加入插件，tests/theme-boot.test.ts 守住）。本机没有 PostgreSQL 时，靠它在浏览器里走完登录、退出、reauth 重放与 SSE；有真实网关时设 PANDORA_API=http://127.0.0.1:8081 之类，vite 改为把 /v1 代理过去，假后端不挂。

成员清单
mock-api.ts: 假后端插件 mockApi(app)：按 api-contract.md 外壳接口返回同形状数据（admin：login / logout / me / reauth / 改密码 / events，外加挂 reauth + 幂等的 POST v1/users/{id}/balance 用来验证重放；portal：login / logout / me / site-config / appearance / 注册两步 / 快捷登录签发与消费 / 余额 / 订阅 / 佣金 / 未读数 / events）；POST /__mock/expire-reauth 让当前会话 rat 立即过期；演示账号 admin@pandora.dev、user@pandora.dev，口令 pandora-dev-pass，邀请码 PANDORA，验证码 123456；状态在内存里，vite 重启即复原

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
