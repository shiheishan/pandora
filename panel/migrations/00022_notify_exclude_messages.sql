-- +goose Up
-- 把 ticket_messages 移出自动通知
--
-- 触发器的路由规则是「行里有 user_id 就推给他」。这条规则对大多数表成立，
-- 因为那个 user_id 就是数据的归属人。但 ticket_messages 的 user_id 是
-- **消息作者**，不是工单归属：客服回复时它是客服的 ID，于是通知被推给了客服，
-- 而真正需要知道「有人回你了」的提问者收不到。
--
-- 这类「归属不等于行内 user_id」的表不能走自动通知，得由 handler 显式推送
-- 到正确的对象（见 admin/handlers.go 里回复后查 TicketOwner 的那段）。
--
-- tickets 表本身保留：它的 user_id 确实是工单归属，状态变更推给他是对的。
DROP TRIGGER IF EXISTS zz_notify_ticket_messages ON public.ticket_messages;

COMMENT ON FUNCTION app.notify_change() IS
  '数据变更通知。只发定位信息（表名/操作/租户/归属/主键），前端据此重新拉取。'
  ' 注意：仅适用于 user_id 表示数据归属的表；若 user_id 是操作者而非归属者'
  '（如 ticket_messages），不能挂本触发器，否则通知会发给错误的人。';
