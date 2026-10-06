# panel/internal/platform/httpx/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

全部网关唯一的响应与错误模型
  - 错误分对外码与对内详情两层：对外只有封闭列表里的错误码与中性中文文案，详情只进日志（SEC-006）
  - 前端 src/core/api.ts 按同一列表解析 {"error":{…}} 信封，message 由页面原样显示
  - 幂等重放靠 PrepareJSON / WritePrepared 写出与首次完全相同的字节。

成员清单
httpx.go: Code 封闭列表与状态映射（reauth_required 与 forbidden 同为 403 不同码；upgrade_required 为 426，只给节点网关的旧 bootstrap，前端 SERVER_ERROR_CODES 不登记）、
  - Error 与构造器 New / Invalid / NotFoundOrForbidden / Internal、
  - JSON / OK / Created / NoContent / Fail 出口、
  - PrepareJSON / WritePrepared、
  - DecodeJSON 严格解码
context.go: 请求 ID、主体 Principal、租户 ID 的 context 存取；ClientIP 是全部网关唯一的来源地址口径：只信反代覆写的 X-Real-IP，不解析 X-Forwarded-For，缺省回落 RemoteAddr
require_user.go: 登录检查出口 RequireUser：主体缺失或 UserID 为空即 Fail 成 401 unauthorized「需要登录」并返回 false，处理器开头的唯一写法
*_test.go: codes_test 守 reauth_required 与 upgrade_required 的状态映射
  - require_user_test 守登录检查的四种拒绝与放行不写响应
  - prepared_test 守预制响应逐字节一致
  - context_test 守 ClientIP 不采信 X-Forwarded-For
  - message_zh_contract_test 扫 panel/internal 全部非测试源码里 httpx.New / Error{Message} / Invalid 的字面量不许纯英文，节点网关整包豁免、与面板共包的节点与支付回调函数按「包目录 + 函数名」豁免，豁免项找不到即失败

法则: 成员完整·一行一文件·父级链接·技术词前置
