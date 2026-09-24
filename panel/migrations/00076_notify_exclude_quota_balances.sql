-- 把 quota_balances 移出自动变更通知（缺陷 15）。
--
-- 触发器的路由规则是「行里有 user_id 就只推给本人，没有就推全租户」。
-- quota_balances 挂在订阅上、没有 user_id，于是每一次节点上报流量都会给
-- 整个租户的在线用户各推一条 subscriptions.changed —— 私有数据的变更信号
-- 变成了全站广播，节点越多、上报越勤，通知通道越堵。
--
-- 不改成按订阅反查归属人：它的写入频率就是流量上报的频率，00020 本来就
-- 规定这类高频表不挂（「流量上报这类高频写入的表……推送它们既没有界面
-- 意义，又会把通知通道淹掉」）。用量数字靠页面拉取刷新即可；订阅本身的
-- 变更（续费、到期、换链接）仍经 subscriptions / subscription_credentials 推送。

-- +goose Up
DROP TRIGGER IF EXISTS zz_notify_quota_balances ON public.quota_balances;

-- +goose Down
DROP TRIGGER IF EXISTS zz_notify_quota_balances ON public.quota_balances;
CREATE TRIGGER zz_notify_quota_balances AFTER INSERT OR UPDATE OR DELETE ON public.quota_balances
  FOR EACH ROW EXECUTE FUNCTION app.notify_change();
