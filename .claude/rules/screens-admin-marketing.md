---
paths:
  - "panel/frontend/src/admin/screens/marketing/**"
---

# 后台 · 营销

- 地址：`/marketing/coupons[/<券 id>]?status=active|paused`（详情弹窗）、`/marketing/gifts/<templates|batches|usages>[/<批次 id>]`、`/marketing/commission/<review|withdraw|settings>?status=open|done|all`、`/marketing/signup`。「注册试用与邀请」直接挂 users 模块的 `SignupGrowth`（无 props，样式自带），本模块不改 users/ 下的文件。
- 文件分工：`logic.ts` 放输入换算与优惠券，`giftModel.ts` 放礼品卡，`commissionModel.ts` 放提现与复核，`levelsModel.ts` 放佣金规则表单与单独设置；各配单测（`marketing.test.ts` / `gifts.test.ts` / `commissionModel.test.ts` / `levelsModel.test.ts`）。
- 只有人民币：券、礼品卡、佣金、提现都没有币种，`formatMoney` 单参。
- 优惠券（10-16）：「新建」「批量生成」在页头 `PageActions`，都在弹窗里（`CouponFormModal`）；列表是 ui `Table boxed`，表尾只有一页时写「共 N 张」、超过一页换成 `<Pager inline>`；手机上适用套餐 / 使用 / 有效期三列 `hideOnMobile`，使用与有效期挪到优惠码下一行（全局类 `m-only`），不可切换的券（已过期、已用完）整行淡显、灰标签（`rowClassName`）；详情（规则 + 最近 200 条兑换）也是弹窗。有开始时间 `valid_from`（空 = 创建即生效）；没有「限定时长」，后端也已删 `applicable_intervals`。批量上限前端 500（`COUPON_BATCH_MAX`），后端 1000，保持现状。
- 礼品卡明文只在两处出现：生码响应里的前 4 张样例（`sample`）与批次的一次性导出 CSV；列表、使用记录、按筛选导出的报表一律掩码。不要在别处展示或拼出明文。
- 「导出当前筛选」走 `GET v1/gift-cards/codes/report`（要 reauth，无幂等），与卡码列表共用 `codeFilterQuery`；确认框写明明文只在批次的一次性导出里。
- 下载文件名优先取 `Content-Disposition`，幂等重放的响应没有这个头，所以兜底按批次 id 前 8 位自拼（`batchFileName` / `codesReportFileName`）。
- 卡码的行内停用 / 恢复不要 reauth：发现异常要能立刻止血（契约有意设计）。
- 礼品卡模板（10-16）：逐行列表，没有编辑入口；只能新建（`TemplateModal`）与行内暂停 / 恢复。暂停恢复是把整份模板原样重发 `POST v1/gift-cards`（`templateStatusBody`，只改 status，别的字段原样带回，要 reauth）。新建只填名称、类型、奖励、仅新用户 / 仅付费用户、每人限兑次数。说明、主题色、需邀请、限定套餐、冷却小时 10-21 前后端连库删了（迁移 00065），不要加回。盲盒建模板时就填奖品（2–50 个）。套餐卡：订阅套餐必须选一档在售周期价，一次性套餐自动取它唯一的在售价；价格后来下架的模板行标「价格已下架」，这时暂停恢复会被后端 422 拒（要重建模板）。
- 后端恒回的字段在 schema 里必填、不写降级分支：`gift-cards/stats` 的 `balance_issued`，模板的 `price_archived`，`commission/overview` 的 `total_earned` / `invited_users` / `scope`（后端缺设置时已兜底 `every_order`）。
- 前端校验的错误键与后端 422 `fields` 同名（各 model 文件的表单构建），改字段名两边一起改。
- 佣金与提现的二级切换是带计数的 `FilterTabs`：待复核佣金（count = `need_review`）、提现申请（count = `waiting_withdrawals`）、规则设置。待复核与提现都是 `Card flush` + kit `RowList`；「待结科目余额不足」那条说明用红字。规则设置是一张 Card 三段（返佣规则 / 三级分销 / 开放提现）+ 底栏保存。
- 提现筛选「处理中 / 已完成 / 全部」：处理中 = `requested,approved`，已完成 = `paid,rejected`，全部不带 status（`queries.ts` 的 `WITHDRAWAL_FILTER_STATUS`，后端 status 收逗号分隔多状态）。提现只有这四态（`withdrawalState`）。「通过」先弹确认框；驳回必填理由（用户可见）。提现被拒两端都叫「已驳回」（按钮「驳回」、理由「驳回理由」），与注销时「驳回提现」同一说法。
- 佣金规则合成一张表单（`CommissionSettings.tsx`，`levelsModel.ts` 的 `settingsToForm` / `buildSettingsConfig`）：返佣比例、冻结期、计佣范围、三级分销开关与三级比例、开放提现开关、最低提现、提现方式，一次 `POST v1/commission/config` 发全部 10 个字段。三级之和与开启全 0 的错误都挂 `distribution_l1`；提现方式开关两种状态都至少选一种（关着时留着的方式在重新开放时直接生效）。关闭提现 = 佣金解冻即自动转入余额。文件名 `levelsModel.ts` 不要和组件文件只差大小写。
- 单独佣金设置（`CommissionOverrides.tsx`，放在规则设置页签表单下面）：按邮箱设，比例或范围留空 = 跟全局，再保存整行覆盖；增删都要 reauth、不带幂等键。
- 打 USDT 提现的款：流水号填链上交易哈希（前端只看形状），实付 USDT 数量可选、十进制字符串；面板不发起链上转账，文案不要暗示会自动打款。
- 页面级说明在 `admin/page-help.ts` 的 `marketing:coupons` / `marketing:gifts` / `marketing:commission`，页面里不写段落说明。
- 10-16 设计稿没画、但不在删除清单里的现有能力保留：单独佣金设置、提现方式、「导出当前筛选」、使用记录按模板筛选。
- 礼品卡（av4）：页头「新建」（模板）在三个子视图都显示；模板是 `RowList`（面额块 + 名称与类型标签 + 一行说明 + 生成卡码 / 暂停恢复）；批次、卡码、使用记录都是 ui `Table`，批次第二行写建批时间与建批人邮箱（`created_by_email`）；批次与卡码表尾同优惠券（一页「共 N 批 / 张」，多页 `<Pager inline>`）；照样稿 `.h-m` 把次要列设 `hideOnMobile`。
