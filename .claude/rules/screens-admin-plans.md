---
paths:
  - "panel/frontend/src/admin/screens/plans/**"
---

# 后台 · 套餐

- 限速是版本上的按用户速率、全程生效、留空不限，与超额策略无关：向导「用量与设备」与版本编辑都有常开的「限速 Mbps」，没有超额策略下拉；保存一律写 `overage_policy: 'suspend'` 并固定说明「流量用完后停止服务」。存量的 throttle / metered_billing 策略读取照收、界面不显示，保存时一并改成 suspend。
- 编辑向导（`PUT complete`）按三态回传：设备、限速、卖点、推荐没改就不带键，清空设备或限速发 `null`（改回不限），填了发正整数；流量、价格、线路仍是 `null` = 不动。改提交体时守住这个区分（`model.ts` 的向导提交体与 `model.test.ts`）。
- 后端保证、前端不另做提示或拦截：额度或线路一变开出的新版本继承当前版本全部设置，上架时间窗保留。
- 「新建版本」后端不复制：前端依次 `POST versions` → `PUT` 复制当前版本语义 → `POST pools` 复制绑定；半途失败要说清草稿已建、复制没做完。
- 不做「恢复上架」：归档确认框写明不可恢复，只想暂停售卖的引导到「销售设置」。
- 卖点最多 5 条（每条 1–40 字、不重复）与「标为推荐」只在向导第 1 步和销售设置抽屉出现，两处共用 `Highlights.tsx`，校验在 `model.ts`。
- 写失败走 `failure.ts` 的 `useCatalogFailure`：只有 422 的 `fields` 给表单（没有表单可标的确认框把 fields 的话 Toast 出来）；409 的 `fields` 是乐观锁现值，标到表单上看不见，Toast 信封原文。不要直接用通用 `useFailure`。
