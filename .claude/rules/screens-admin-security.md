---
paths:
  - "panel/frontend/src/admin/screens/security/**"
---

# 后台 · 安全与运维

- 「访问日志」是 audit_events 与订阅拉取日志的归并（安全事件流），不是 HTTP 访问日志：没有方法、路径、状态码与耗时，设计稿的列按契约换义；没有 total，只能按「这页满没满」翻更早。
- 这几张表都不在 SSE 监听里，「实时尾随」是第一页 5 秒轮询；只有降级开关按 `meta.topics` 接 `switches.changed`。
- 降级开关极性：`enabled` = 功能可用，设计稿的「开启『暂停…』」对应 `enabled=false`。进入降级时原因必填（数据库 CHECK，后端回 409 而不是 422），前端先拦；恢复时可选。
- 批量停用走 `suspended`（可恢复），已停用 / 已封禁 / 后台账号由后端跳过：结果按 `disabled` / `skipped` 汇总，一个都没停成时后端不写结论、前端缓存补丁也不写。
- 产品取舍：「订阅下发使用缓存」开关不做；字典不收 `ops.bulk_export` / `ops.reports` / `node.autoscale`（旧库残留的行按未知 code 显示、照后端极性可切）。
- 只读演示账号没有 `security.audit.read`，整个模块不可见；审计导出另要 `ops.export` + reauth，导出日期校验与后端 `auditExportRange` 同键同文案。
