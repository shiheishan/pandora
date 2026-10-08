-- +goose NO TRANSACTION
-- 订阅备注名在同一用户名下不重名（不区分大小写）（购买模型统一 Q7，用户 2026-10-07 定；w7buya）。
--
-- 重名的两份在 App 里分不清（配置名都是「站点名 · 名字」）。subscription.SetLabel 撞这条唯一索引
-- 回 409「这个名字已经用在『…』上了」；履约时名字被占（付款期间另一份改成了同名）由
-- billing provisionSubscription 自动加「 2」「 3」后缀，不让结算失败。
--
-- subscriptions 是大表：CONCURRENTLY + NO TRANSACTION，会话级 SET、段尾 RESET。中途失败会留下
-- INVALID 索引：Up 用 IF NOT EXISTS 可重入，按 MIGRATION-RUNBOOK 第 1 节第 5 步先
-- DROP INDEX CONCURRENTLY IF EXISTS 再重跑。00135 刚加的列全为空，部分索引是空的，建得很快：5k 副本上约 15ms。

-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS subscriptions_user_label_unique
  ON public.subscriptions (tenant_id, user_id, lower(label))
  WHERE label IS NOT NULL;

RESET statement_timeout;
RESET lock_timeout;

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS public.subscriptions_user_label_unique;

RESET statement_timeout;
RESET lock_timeout;
