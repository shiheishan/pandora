-- irreversible: 纯数据修正。把仍是占位值的冻结天数 7、最低提现 1000 改成 3 与 10000，改之前的值没有留存，事后分不清哪些行是这条迁移改的、哪些是管理员自己设成 3 / 10000 的，Down 无从还原。
-- forward-fix: 新值对旧版本同样合法，回滚到 00028 不需要动这两条设置；确实要改回，由管理员在后台「系统设置」里按需调整。要整体回到 00029 之前，走升级前备份恢复（panel/deploy/MIGRATION-RUNBOOK.md 第 3 节）。
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

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
  RAISE EXCEPTION
    'rollback refused (00029 commission defaults): the pre-update values were not kept; adjust the settings forward in the admin panel, or restore the pre-upgrade backup';
END
$$;
-- +goose StatementEnd
