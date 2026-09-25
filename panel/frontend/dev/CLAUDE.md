# panel/frontend/dev/
> L2 | 父级: /panel/frontend/CLAUDE.md

只在 vite serve 时存在的开发期设施，永不进产物（vite.config.ts 只在 command === 'serve' 时加入插件，tests/theme-boot.test.ts 守住）。本机没有 PostgreSQL 时，靠它在浏览器里走完登录、退出、reauth 重放、SSE 与各页面的接口；外壳在 mock-api.ts，模块接口按入口拆在 mock/ 下，各页面的会话只改自己的文件；有真实网关时设 PANDORA_API=http://127.0.0.1:8081 之类，vite 改为把 /v1 代理过去，假后端不挂。

成员清单
mock-api.ts: 假后端外壳 mockApi(app)：持有账号、会话与 rat、幂等表（只重放 2xx，非 2xx 同键重新执行，R85），答外壳接口（admin：login / logout / me / reauth / 改密码 / events；portal：login / logout / me / site-config / appearance / 注册两步 / 快捷登录消费 / events），其余按入口依次询问 mock/admin 或 mock/portal，未匹配回 404 信封；为每个模块请求造 MockContext（三个守卫与非 JSON 响应的实现在这里）；POST /__mock/expire-reauth 让所有会话 rat 立即过期；演示账号 admin@pandora.dev（契约里的全部权限码）、viewer@pandora.dev（六个读权限，实测按权限隐藏）、user@pandora.dev，口令 pandora-dev-pass，邀请码 PANDORA，验证码 123456；状态在内存里，vite 重启即复原
mock/: 处理器契约 types.ts 与按入口拆分的模块假接口；见 mock/CLAUDE.md

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
