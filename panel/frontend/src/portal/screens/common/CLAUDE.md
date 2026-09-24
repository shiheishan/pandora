# panel/frontend/src/portal/screens/common/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

门户多个页面共用的读模型与小块，归门户前端会话。只有一个页面用的东西仍放在那个页面自己的目录里；这里的东西若后台也要（如字节换算、按时区出日期），报告协调会话决定是否提升到 core/ 或 ui/，不在这里给后台用。
数据层一律 useApi + react-query，schema 按 api-contract.md 写全写严：「现有」字段必填，「待补·后端」字段可选，页面对缺席做降级而不是放宽 schema；实时失效只靠 meta.topics。同一接口与外框 queries.ts 共用查询键；外框 schema 只收外框字段的，页面用子键（subscriptions.ts 的 detail）。
日期显示统一按按日用量接口回的切日时区（修订 R48 / R50），与用量柱同一口径；拿不到时退回浏览器本地时区。

成员清单
subscriptions.ts: 订阅数据层——GET v1/me/subscriptions（子键 detail）、subscription-links、{id}/nodes、{id}/usage、GET v1/me/traffic-packs 的 schema 与查询，rotate 的 mutation（成功后先写回新地址再重拉统计）；pickPrimary / liveSubscriptions / canRenew，usePlanTraffic 汇总一条订阅的流量摘要、重置时刻与切日时区
orders.ts: 订单读模型——GET v1/orders 行 schema（kind / status 枚举）、待支付查询、契约门户-04 的标题映射 orderTitle 与周期文案、按 expires_at 倒数的 expiryNote；第 ③ 步订单页在此扩展
announcements.ts: GET v1/me/announcements 的 schema 与查询（published_at 可为 null，见注释）；第 ⑤ 步消息页复用
traffic.ts: 纯函数——字节按 1<<30 换 GB、到期与重置、按切日时区出日期、额度行选择与摘要（剩余含流量包）、「按目前的速度…」预测、用量柱（补「未到」）
clients.ts: 一键导入深链（Clash Verge / Shadowrocket / Hiddify / Stash / sing-box；v2rayN 无 scheme 改复制）、协议展示名、倍率标签、copyText（非安全上下文退回 execCommand）
Blocks.tsx: Slot（外观插槽，服务端净化过的 HTML，空则不占位）与 LoadError（卡片内失败态 + 重试，断网单独一句）
UsageCard.tsx: 「本期用量」卡：柱状图（今天朱砂、未到灰色矮柱、超 40 根收窄间距）、日均 / 今天、预测与「买流量包 →」
common.module.css: 插槽与用量卡样式
common.test.ts: traffic / orders / clients / subscriptions schema 与 subs/labels 的单元测试，跨时区稳定（UTC、洛杉矶、上海、奥克兰实测）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
