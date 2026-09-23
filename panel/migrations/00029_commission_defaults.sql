-- +goose Up
-- 分销默认值改成实际要用的：冻结 3 天、最低提现 100 元。
--
-- 上一版的 7 天 / 10 元是占位值。冻结 3 天足够覆盖绝大多数
-- 退款与拒付的发起窗口，又不至于让代理觉得钱被压太久；
-- 100 元的门槛是为了让打款笔数可控 —— 每笔提现都要人工核对收款信息，
-- 一块两块也要走一遍流程的话，审批会变成没人愿意干的活。
--
-- 只改默认值，不覆盖管理员已经调过的设置。

UPDATE system_settings SET value = '3'::jsonb, updated_at = now()
 WHERE key = 'commission.freeze_days' AND value = '7'::jsonb;

UPDATE system_settings SET value = '10000'::jsonb, updated_at = now()
 WHERE key = 'commission.min_withdraw' AND value = '1000'::jsonb;
