# panel/frontend/src/portal/screens/tickets/
> L2 | 父级: /panel/frontend/src/portal/screens/CLAUDE.md

工单支持（用户门户-07-工单.dc.html；契约门户-07，修订 R25、R60；D-F-2 已决）。地址驱动：#/tickets 列表（宽屏右侧显示第一张，一张都没有时直接给新建表单）、#/tickets/new[?order=<订单 id>]（订单页明细末尾的「提交工单」带 order 预填）、#/tickets/<id>；宽屏左列 240–320 的列表 + 右列，< 640 列表与会话分屏，会话头有「全部工单」返回。
状态按契约映射，closed 靠 closed_reason 分「已撤回」（半透明）与「已关闭」；旧后端缺 closed_reason 时一律「已关闭」。按钮可见条件与后端判定一致：撤回要未关闭且客服没回复过，关闭与回复只要未关闭（resolved 仍可回复、会重开）。客服按 D-F-2（已决，5.A.2）统一显示「客服」，系统消息居中灰字。
写操作：新建、回复、关闭幂等（support_ticket_create / _user_reply / _user_close），一次意图一个键，成功或 4xx 后丢弃；撤回不幂等、必须带 {}。撤回与关闭都先 ConfirmModal（关闭不可逆，设计稿没有确认框，按规则补上）。实时 ticket.updated / tickets.changed 失效列表与详情。
设计稿之外：分类按接口六类渲染（默认 general）；表单补「关联订单（可选）」下拉（最近 20 张 + 预填的那张）；正文按后端要求必填 ≥ 10 字；页头副标题不承诺「10 分钟内首次响应」（后端 SLA 普通 12 小时）；回复框回车发送，输入法组字时的回车不算。

成员清单
index.tsx: 页面组件——列表（TicketItem）、会话（TicketView / Thread：头部与关联订单、撤回与关闭确认、消息、ReplyBox）、新建（NewTicket：分类胶囊、OrderPicker、标题、详细描述）
api.ts: 数据层——分类、列表、详情 schema 与查询，最近订单，新建 / 回复 / 关闭 / 撤回四个 mutation，写后失效列表与该工单
model.ts: 纯逻辑——ticketRoute、ticketStatus、canWithdraw / canClose / canReply、authorLabel、messageTime、validateTicket
Tickets.module.css: 页面样式，取自设计稿门户-07（< 640 按 data-view 分屏）
tickets.test.ts: 第 ⑤ 步工单的单元测试（schema、子路由、状态与按钮、作者、消息时间、表单校验）

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
