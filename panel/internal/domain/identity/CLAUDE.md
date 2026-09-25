# panel/internal/domain/identity/
> L2 | 父级: /panel/internal/domain/CLAUDE.md

身份域：注册、登录、会话与口令，是所有网关鉴权的上游。只依赖 platform（crypto/db/httpx/audit/token）与 plugin 的事件发射；需要外部能力时（注册验证码投递）经本包定义的接口注入，不 import 其它业务域。凭据一律只存哈希，明文只在签发那一刻返回一次。

成员清单
service.go: Service 与构造、VerificationMailer 注入点；注册两步（StartRegistration 同事务写验证码并经 mailer 入队，提交后 Kick；邮箱已存在时响应一致但不入队，IAM-006）、Login 与会话签发
registration_policy.go: 注册总开关 feature_switches.auth.registration 与 auth.registration_mode（closed/invite_only/open）的判定，缺配置按关闭
invite.go: 邀请码绑定「谁邀请了谁」，不发奖励
sessions.go: 门户自助会话列表与吊销，只触及 audience=public 的会话（后台会话不可见、不可踢）；last_seen_at 只读，写入点在认证中间件
logout.go: 退出当前会话，会话与整条 refresh 链同事务吊销
password.go: 自助改密，吊销既有凭据（门户保留当前会话与其 refresh 令牌，admin 全部吊销）；口令错误的审计先于事务错误提交；admin 域新密码至少 12 位（validatePasswordFor），门户仍 8 位
admin_profile.go: 管理员展示信息 AdminProfile（GET v1/me 追加的邮箱、显示名与生效角色），角色过滤与 admin 登录展开权限一致
admin_reset_password.go: 管理员替用户设新密码；原因可选（R101），给了才进审计摘要
reauth.go: 用口令换一枚 rat 刷新过的令牌，供 RequireRecentReauth 保护的高危路由使用
quicklogin.go: 已登录设备生成 60 秒一次性快捷登录链接
*_test.go: 单元与契约测试；*_pg18_test.go 共用 logout_pg18_test.go 的 openLogoutPG18Fixture（run-pg18-gates.sh 的 logout 域），含会话 audience 隔离与注册验证码投递全链路

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
