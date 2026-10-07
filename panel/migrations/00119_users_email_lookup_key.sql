-- 登录按邮箱查口令不再全表扫描（w3core）。
--
-- users.email 是 citext，唯一约束 (tenant_id, email) 的索引本该一步定位到人。但运行
-- 角色受 RLS 约束：查询自己的 WHERE 条件只有 LEAKPROOF 的才能被下推到策略条件之前、
-- 用作索引条件，而 citext 的等号（citext_eq）不是 LEAKPROOF。于是索引只能用上
-- tenant_id 那一列，租户里的每个用户都要被取出来再逐行比邮箱：5k 用户时登录查口令
-- 平均 18.2ms，5k-r3 第二次 seed 后用户翻倍，涨到 94.6ms。
--
-- 做法：加一个存储型生成列 email_lower = lower(email::text)，在 (tenant_id, email_lower)
-- 上建索引。查询写成 email_lower = lower($2)：等号是 text 的 texteq（LEAKPROOF），
-- 另一边的 lower() 只作用在参数上、不涉及行数据，整条条件因此算 LEAKPROOF，可以作为
-- 索引条件。不引入 SECURITY DEFINER，也不给任何函数标 LEAKPROOF，RLS 原样生效。
--
-- 与 citext 的比较口径一致：citext 比较时就是把两边按库的缺省排序规则 lower() 之后再比。
-- 调用方仍保留 email = $2（citext）作为附加过滤，结果集与改前逐行相同。
-- 唯一性仍由原来的 users_tenant_email_unique 保证，这个索引不设唯一，免得多一种失败。
--
-- 必须写 STORED：PG18 的生成列缺省是 VIRTUAL，虚拟列不能建索引。加列会重写 users 表
-- （5k 级几十毫秒）；拿不到表锁 5 秒即失败重来，不排在业务事务后面堵住登录。

-- +goose Up
SET LOCAL lock_timeout = '5s';

ALTER TABLE public.users
  ADD COLUMN email_lower text GENERATED ALWAYS AS (lower(email::text)) STORED;

CREATE INDEX idx_users_tenant_email_lower ON public.users (tenant_id, email_lower);

-- +goose Down
SET LOCAL lock_timeout = '5s';

DROP INDEX IF EXISTS public.idx_users_tenant_email_lower;
ALTER TABLE public.users DROP COLUMN IF EXISTS email_lower;
