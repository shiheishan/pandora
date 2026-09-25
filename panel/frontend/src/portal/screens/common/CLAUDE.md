# panel/frontend/src/portal/screens/common/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

门户多个页面共用的读模型与小块，归门户前端会话。只有一个页面用的东西仍放在那个页面自己的目录里；这里的东西若后台也要，报告协调会话决定是否提升到 core/ 或 ui/，不在这里给后台用（字节换算已由后台前端一提升为 core/format 的 formatBytes，这里只包一层排版用的 bytesParts / compactBytes）。
数据层一律 useApi + react-query，schema 按 api-contract.md 写全写严：「现有」字段必填，「待补·后端」字段可选，页面对缺席做降级而不是放宽 schema；实时失效只靠 meta.topics。同一接口与外框 queries.ts 共用查询键：订阅列表的全字段 schema 与查询住在 queries.ts，这里转出。
日期显示统一按按日用量接口回的切日时区（修订 R48 / R50），与用量柱同一口径；拿不到时退回浏览器本地时区。

成员清单
subscriptions.ts: 订阅数据层——转出外框的订阅列表查询与 pickPrimary / liveSubscriptions，自有 subscription-links、{id}/nodes、{id}/usage、GET v1/me/traffic-packs 的 schema 与查询，rotate 的 mutation（成功后先写回新地址再重拉统计）；canRenew，usePlanTraffic 汇总一条订阅的流量摘要、重置时刻与切日时区
catalog.ts: 商品目录——GET v1/plans（带令牌看组专属价）、GET v1/traffic-packs、GET v1/payment-methods（只留 CNY）的 schema 与查询；周期归档 periodOf（(month,3)|(quarter,1) 为季、(year,1)|(month,12) 为年）与文案、折合月价、省额与最小省幅、每 GB 单价、重置与额度周期文案
orders.ts: 订单读模型——GET v1/orders 行 schema（kind / status 枚举）、概览待支付与订单页待支付卡片（draft / pending_payment / processing 多值）查询、按筛选分段加载的 useOrderPages（每页 6、offset 递增）、取消 mutation；契约门户-04 的标题、状态徽标、按月分组与已支付合计、展开区结果与事实行；GET v1/orders/{id} 明细 schema 与 useOrder（可轮询到终态）；四个下单接口同形的 201 schema
announcements.ts: GET v1/me/announcements 的 schema 与查询（published_at 可为 null，修订 R71）；第 ⑤ 步消息页复用
traffic.ts: 纯函数——bytesParts / compactBytes（包 core formatBytes）、到期与重置、按切日时区出日期、额度行选择与摘要（剩余含流量包）、「按目前的速度…」预测、用量柱（补「未到」）
clients.ts: 一键导入深链（Clash Verge / Shadowrocket / Hiddify / Stash / sing-box；v2rayN 无 scheme 改复制）、协议展示名、倍率标签、copyText（非安全上下文退回 execCommand）
PayFlow.tsx: 支付弹窗（外壳的支付弹窗）：choose 态给已有的待支付订单选支付方式（选完在弹窗内转 redirect，POST 渠道失败可「换一种支付方式」），redirect 态发起 POST v1/orders/{id}/pay 后顶层 GET 导航去收银台（POST 跳转按暂不可用，CSP form-action 会拦表单），done 态显示按订单种类的成功文案，confirm 态在收银台回跳后 3 秒轮询订单、最多 2 分钟；成功后失效 portal 前缀下全部查询
PayFlow.module.css: 支付弹窗样式，去掉设计稿的二维码块
intent.ts: useIntentKey / createIntentKey——按请求指纹给幂等键，同样的请求复用、改了参数换新键，reset() 结束一次意图（成功或 4xx 业务拒绝后丢弃键，免得稍后同样的请求被回放那次结果）；结账、充值、兑换礼品卡、佣金转余额与提现共用（前三者尚未调用 reset）
Blocks.tsx: Slot（外观插槽，服务端净化过的 HTML，空则不占位）与 LoadError（卡片内失败态 + 重试，断网单独一句）
UsageCard.tsx: 「本期用量」卡：柱状图（今天朱砂、未到灰色矮柱、超 40 根收窄间距）、日均 / 今天、预测与「买流量包 →」
common.module.css: 插槽与用量卡样式
common.test.ts: traffic / orders / clients / subscriptions schema 与 subs/labels 的单元测试，跨时区稳定（UTC、洛杉矶、上海、奥克兰实测）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
