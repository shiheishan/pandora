# panel/internal/platform/httpx/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

全部网关唯一的响应与错误模型。错误分对外码与对内详情两层：对外只有封闭列表里的错误码与中性中文文案，详情只进日志（SEC-006）；前端 src/core/api.ts 按同一列表解析 {"error":{…}} 信封，message 由页面原样显示（R116）。幂等重放靠 PrepareJSON / WritePrepared 写出与首次完全相同的字节。

成员清单
httpx.go: Code 封闭列表与状态映射（reauth_required 与 forbidden 同为 403 不同码）、Error 与构造器 New / Invalid / NotFoundOrForbidden / Internal、JSON / OK / Created / NoContent / Fail 出口、PrepareJSON / WritePrepared、DecodeJSON 严格解码
context.go: 请求 ID、主体 Principal、租户 ID 的 context 存取
*_test.go: codes_test 守 reauth_required；prepared_test 守预制响应逐字节一致；message_zh_contract_test 扫 panel/internal 全部非测试源码里 httpx.New / Error{Message} / Invalid 的字面量不许纯英文，节点网关整包豁免、与面板共包的节点与支付回调函数按「包目录 + 函数名」豁免，豁免项找不到即失败

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
