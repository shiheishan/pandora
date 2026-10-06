# panel/frontend/src/admin/screens/marketing/
> L2 | 父级: /panel/frontend/src/admin/screens/CLAUDE.md

营销（后台-06）：优惠券、礼品卡、佣金与提现三个标签
  - 视觉按 管理后台-06-营销.dc.html，数据与规则按 api-contract.md 后台-06
  - 设计稿没有、按契约「待补·前端」补的：券的门槛 / 封顶 / 每人限用 / 有效期 / 适用套餐与批量的活动名称、礼品卡的模板编辑抽屉与生码参数、提现的拒绝理由与转账流水号、已过期 / 已用完 / 打款失败等状态。
分层：schemas（zod，与后端对账的唯一防线）→ queries（react-query 读、权限、写失败统一处理、幂等键）→ logic（纯函数，单测守住）→ 组件
  - 写接口的 reauth 由常驻对话框接管，取消时静默
  - 要幂等的接口一次用户意图一个键，改了请求体换新键。
权限：标签读权限由 Shell 判
  - 页面内再按写权限（marketing.coupon.write / giftcard.write / withdrawal.approve / commission.write）隐藏按钮，兑换记录另要 billing.order.read，套餐名与套餐卡要 catalog.read（没有时退回「N 个套餐」、套餐卡不可编辑）。
礼品卡明文只在两处出现：生码响应里的前 4 张样例与一次性导出的 CSV
  - 列表、使用记录一律掩码
  - 导出经 core/api 的 requestRaw 拿 CSV，重放不带 Content-Disposition，文件名按批次 id 前 8 位自拼
  - 批次卡码可按状态筛选，「导出当前筛选」走 GET v1/gift-cards/codes/report（marketing.giftcard.read + ops.export 才显示按钮，reauth 由对话框接管，无幂等），与列表共用 codeFilterQuery，只出掩码报表，确认框写明明文只在批次的一次性导出里。
后端恒回的字段在 schema 里必填，不写降级分支：
  - gift-cards/stats 的 balance_issued、commission/overview 的 total_earned / invited_users / scope（后端缺设置时兜底 every_order）
  - commission/config 恒带 scope。

成员清单
index.tsx: 页面入口，按 tab 切 Coupons / Gifts / Commission，rest 交给礼品卡（#/marketing/gifts/<templates|batches|usages>[/<批次 id>]）
schemas.ts: 全部接口的 zod schema 与类型；后端必回字段必填，Go 的 omitempty 可选，nil 切片 nullable 归一成 []
queries.ts: 查询键前缀 MK、各读 hook（券、兑换记录与佣金总览挂 orders.changed，套餐目录挂 plans.changed，其余营销表没有变更通知）、useInvalidateMarketing（写后整前缀失效），并转出 admin/actions.ts 的 useCan / useFailure / useIntentKey
logic.ts: 纯函数——
  - 元 / 百分比与分 / 万分比互转（多于两位小数判非法）、优惠与用量文案、券 / 卡码 / 提现状态映射、礼品卡面额与兑换内容、批次名 GB-MMDD-XXXX、卡码筛选查询串 codeFilterQuery 与掩码报表文件名、四张表单到请求体的构建与前端校验（错误键与后端 fields 同名）
Coupons.tsx: 优惠券标签：状态分段、六列列表、行内启停开关、点码展开兑换记录、批量生成结果弹窗与前端拼的 CSV
CouponForm.tsx: 新建单张 / 批量生成共用的内联表单；409 码已存在落在码输入框
Gifts.tsx: 礼品卡标签：四个统计、模板卡片网格、批次左栏 + 掩码卡码右栏（按状态筛选、导出当前筛选的掩码报表、行内停用 / 恢复，不要 reauth）、使用记录（按模板筛）
GenerateCodes.tsx: useBatchExport（一次性导出）、useCodesReport（按筛选导出掩码报表）、GenerateModal（数量 / 前缀 / 有效期）、OneTimeModal（仅此一次可见，关闭后跳到该批次）
TemplateDrawer.tsx: 模板新建 / 编辑抽屉：卡型（编辑时锁定）、奖励（通用 / 套餐 + 价格 / 盲盒奖池 2–50）、领取条件、限制、主题色、状态
Commission.tsx: 佣金与提现标签：四个统计、提现列表（通过直接提交、拒绝要理由、打款要流水号且带幂等键）、邀请与佣金设置
marketing.module.css: 模块公共样式（栈、工具条、面板、表单网格）；统计条、分页、三态容器用 ui 的 StatStrip / Pager / QueryView
Coupons.module.css / Gifts.module.css / Commission.module.css: 各标签独有的布局
marketing.test.ts: logic 与 schema 边界的单元测试

法则: 成员完整·一行一文件·父级链接·技术词前置
