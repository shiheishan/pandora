-- 订单主动查单的巡检排程（PAY-009 降级补偿）。
--
-- 支付到账原先只靠渠道回调；回调丢了（渠道故障、我方 502、防火墙拦截）就没人知道。
-- 现在 aegis-public 定时挑出「发起过支付、过了一段时间还没到账、订单还没过期」的
-- 在途支付意图，逐个向渠道查单，查到已付就走与回调同一条结算主链补记
-- （billing/payment_query.go）。
--
-- 巡检要记两件事，都挂在支付意图上，不另建表：
--   - query_attempts：巡检已查过几次，到上限就不再查（防止对同一单无限重查）；
--   - next_query_at：下一次最早什么时候查，按次数指数退避。为空表示还没查过，
--     首查时间由代码按 created_at + 等待时长算。
-- 认领一行时在同一个短事务里把 next_query_at 推到未来，再提交、再去打渠道：
-- 多个 aegis-public 实例并发巡检时，认领用 FOR UPDATE SKIP LOCKED 互相跳过，
-- 提交后别的实例按 next_query_at 过滤也看不到它，同一单不会被并发查询，
-- 也不会在打渠道的 HTTP 往返期间占着行锁挡住真实回调的结算。
--
-- 后台手动查单与门户「我已支付」不读写这两列：它们是人点的，有各自的限流。
--
-- 两列只是排程状态，不是资金证据，回滚直接删列。

-- +goose Up

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE public.payment_intents
  ADD COLUMN query_attempts integer NOT NULL DEFAULT 0
    CONSTRAINT payment_intents_query_attempts_check CHECK (query_attempts >= 0),
  ADD COLUMN next_query_at timestamptz;

GRANT UPDATE (query_attempts, next_query_at) ON public.payment_intents TO aegis_app;

-- +goose Down

SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 删列会连带收回列级授权
ALTER TABLE public.payment_intents
  DROP COLUMN next_query_at,
  DROP COLUMN query_attempts;
