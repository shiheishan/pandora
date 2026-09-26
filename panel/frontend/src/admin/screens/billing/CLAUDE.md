# panel/frontend/src/admin/screens/billing/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

订单与收款（管理后台-05-订单与收款.dc.html）。四个标签，状态都在地址上：「订单」#/billing/orders/<订单 id>?s=<分段>&q=&o=&user_id=&new=（列表 + 480 抽屉，?new=1 或 ?new=<用户 id> 打开人工开单弹窗，用户抽屉的「为其开单」与订单深链都落在这里）、「挂账」#/billing/arrears?s=&o=、「支付渠道」、「收入调整」#/billing/adjust?c=<币种>。
数据流：schemas（纯 zod，订单列表行也是用户详情「最近订单」的形状，users/api.ts 从这里取）→ api（订单查询挂 orders.changed，写后按 BK 前缀整体失效，影响用户余额与订阅的写同时失效用户模块）→ model（纯函数，model.test.ts 守住）→ 组件。订单词汇（ORDER_STATUS_VIEW、orderWhat）与元转分 parseYuan 沿用 users/model，周期文案沿用 plans/model。写失败直接走 actions.ts 的 useFailure：取消订单与凭证号重复的 message 已由后端给中文（R114），原样 Toast。
保留规则 6：设计稿的「欠费单」按后端「挂账」语义做——订单取消后才到账、续费或变更时订阅已结束（R117）或超额扣款、平台欠用户的钱，「转入余额」是贷记，合计按币种分开（R3）。渠道开关按 PAY-009 只动 accepting_new（关 = 结账页不显示、回调照常），「完全停用」放「更多」菜单；系统内置的 offline 只读。
权限分层：billing.order.read 看订单；billing.payment.read 才请求支付记录与渠道；billing.ledger.read 看挂账与收入调整；billing.order.write 才有人工开单、标记已支付、取消（人工开单与标记已支付挂 reauth，取消不挂）；billing.adjustment.write 才能转入余额、登记与冲销调整；billing.provider.write 才能启停渠道。reauth 由外框对话框接管，取消时静默；幂等键按 actions.ts 的 endsIntent 去留。
D-C-3 已决（5.A.2）：不提供「从余额扣除」，弹窗里说明先调账再赠送。契约缺口：列表行没有人工单标识，渠道列只能在详情里显示「人工」。

成员清单
index.tsx: 页面入口，按标签分发到四个组件，相对时间与账龄的 now 每分钟走一次
schemas.ts: 封闭枚举（订单 9 态与 7 种 kind、支付尝试 / 入账 / 退款状态、挂账状态与三种成因，R117 加 ineligible_subscription）与 zod schema：订单列表行（R63）、详情与订单项、支付记录、取消 / 人工开单 201 / 标记已支付（R2 snake_case）响应、挂账（R3 pending_amounts，omitempty 键写 optional）、渠道（R66）、收入调整（reversal_of omitempty、R66 登记人）
api.ts: 读 hook（useOrders、useOrder、useOrderPayments、useUserPick、useLatePayments、useProviders、useAdjustments）、ORDERS_PAGE / LATE_PAGE、BK 查询键前缀与 useInvalidateBilling，转出 schemas
model.ts: 纯逻辑：状态分段到多值 status、渠道兜底（余额 / 人工）与来源、可标记 / 可取消、抽屉 facts、支付记录合并（以支付尝试为行、有入账看入账、线下入账标人工确认、退款附后）、原因与凭证号预检、人工开单价格选项 / 预检 / 提交体、挂账文案 / 账龄 / 按币种合计、渠道开关映射与备注、收入调整预检 / 提交体 / 视图 / 默认冲销原因
model.test.ts: 上述纯逻辑与 schema（omitempty、封闭枚举、R2 / R3 形状）的单元测试
OrdersTab.tsx: 「订单」标签：搜索（防抖）、状态分段、按用户筛选的标签、人工开单入口、七列表格（最小宽 820，960 下卡片内横向滚动）与分页；挂 OrderDrawer 与 ManualOrder
OrderDrawer.tsx: 订单抽屉：头部、facts（用户可点到用户抽屉）、多项订单的订单项、支付记录、待支付单的「手工标记已支付」（凭证号 + 收款说明，确认后提交；重复凭证与订阅已结束进挂账都回 409，文案原样 Toast，R117）、底部「取消订单」（取消原因必填、带 state_version）
ManualOrder.tsx: 人工开单弹窗：用户搜索选择器（可按 ?new=<用户 id> 预选）、套餐与周期（GET v1/plans 的在售价格，CNY 在前）、结算方式（待用户支付 / 线下已收款 + 凭证号 / 赠送）、开单原因；成功后打开新订单抽屉
ArrearsTab.tsx: 「挂账」标签：说明条与待处理合计、状态分段与分页、表格（用户与成因叠成一格、金额、账龄、状态）、「转入余额」确认框（处理原因必填）
ProvidersTab.tsx: 「支付渠道」标签：渠道卡（开关、今日成交 / 24 小时成功率 / 币种、备注）、「更多 → 完全停用」、启停确认（未配置凭据时提醒）
AdjustTab.tsx: 「收入调整」标签：币种筛选、行内登记表单（币种、金额可为负、原因、生效日不晚于今天）与确认、列表（原因与登记人 · 时间叠成一格、生效日、币种、金额、冲销状态）、冲销确认框（原因默认「冲销：原因」）
Billing.module.css: 唯一样式表，数值取自设计稿；表格最小宽只给订单表，收入调整只让列表横向滚动、表单可换行

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
