# panel/internal/
> L2 | 父级: /panel/CLAUDE.md

依赖方向自上而下：api → domain → platform，middleware 挂在 api 之前只依赖 platform。已核对：domain 与 platform 不 import api，platform 不 import domain，middleware 不 import domain，api import domain 33 处。api 只做路由、鉴权与 DTO，domain 承载用例与状态机，platform 提供无业务语义的基础设施。

成员清单
api/: 三个域网关的 HTTP 路由与处理器，admin 29 文件、public 13 文件、node 3 文件；见 api/CLAUDE.md
domain/: 13 个业务域包共 91 文件，billing、nodefabric、dbbackup 最重；见 domain/CLAUDE.md
middleware/: auth.go 四域独立 HMAC 令牌校验与租户注入；idempotency.go 数据库持有的幂等键声明、业务完成绑定与资源绑定；middleware.go 限流、超时、公共链装配；*_test.go 含 PG18 幂等与严格限流测试
platform/: 17 个基础设施包共 138 个非测试 Go 文件，clientauth 占 107；webapp 为 admin/public 两个网关在根 / 下发面板前端；见 platform/CLAUDE.md

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
