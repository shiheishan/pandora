# 面板待办清单

面板重构时的前后端接口契约（原在 `panel/docs/redesign/`）已删除，以后以代码为准。删除前（2026-10-06）逐条核对过契约里的全部「待补」标记、第 5 节待决与定案（D-x-n）、第 7 节既有缺陷；下面只列核对时仍未实现或只实现了一部分的条目。2026-10-13 换栈（P7）后按新代码复核：原第 1、2 条已修，原第 3、4 条随旧代码删除不再适用（新栈 node 模块没有「换池冻结」这一概念，`PATCH` 改池不另做冻结检查）。

「出处」指原契约的节号或条目，原文可在 git 历史里按契约路径查。

## 前端

1. **设备策略「接近上限」按邮箱找用户**
   - 要做：改为按 `user_id` 直接打开用户抽屉。
   - 现状：后端 `GET v1/devices` 每行已带 `user_id`（`panel/internal/module/node/handler/devices.go` 的 `UserID` 字段），但 `panel/frontend/src/admin/screens/users/DevicePolicy.tsx` 仍用 `find(row.email)` 搜索后再打开。
   - 出处：§8.2「其他发现」（后台-03 用户 · 设备策略）。

2. **续费下单遇 409「所选价格已下架」不切换到改价视图**
   - 要做：续费提交回 409 价格已下架时，重新拉取订阅，进入改价视图：只列当前有效价格，默认选同周期，订单预览显示「原价格已调整」。
   - 现状：下单页在 `panel/frontend/src/portal/screens/checkout/Confirm.tsx` 的提交逻辑里只对 `quote_changed` 重新拉报价，其他 409（包括 catalog 的 `ErrPriceArchived`「该价格已下架」，见 `panel/internal/module/catalog/service/service.go`）经 `refusalOf` 显示后端原话。
   - 出处：§4 门户-03「POST v1/me/subscriptions/{id}/renew」待补·前端。
