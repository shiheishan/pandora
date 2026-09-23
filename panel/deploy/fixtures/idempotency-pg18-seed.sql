-- idempotency 域 PG18 测试的 fixture。
--
-- 这个测试用一组固定 UUID 的租户和操作者，但自己从不创建它们——它原本
-- 期望调用方预置，而它一直没有 runner，于是每次都撞在 idempotency_keys
-- 的 tenant 外键上，报成 500 或 foreign key violation，看着像中间件坏了。
--
-- 两个租户是刻意的：测试要验跨租户隔离，B 租户的存在本身就是断言的一部分
-- （能插进去、但在 A 的会话里看不见）。
INSERT INTO tenants (id, slug, display_name, default_currency) VALUES
  ('91000000-0000-7000-8000-000000000001', 'idem-pg18-a', 'Idempotency PG18 A', 'CNY'),
  ('92000000-0000-7000-8000-000000000001', 'idem-pg18-b', 'Idempotency PG18 B', 'CNY');

INSERT INTO users (id, tenant_id, email, display_name, status) VALUES
  ('91000000-0000-7000-8000-000000000011', '91000000-0000-7000-8000-000000000001',
   'idem-a1@pg18.invalid', 'Idempotency A1', 'active'),
  ('91000000-0000-7000-8000-000000000012', '91000000-0000-7000-8000-000000000001',
   'idem-a2@pg18.invalid', 'Idempotency A2', 'active'),
  ('92000000-0000-7000-8000-000000000011', '92000000-0000-7000-8000-000000000001',
   'idem-b1@pg18.invalid', 'Idempotency B1', 'active');

-- 历史遗留的 idempotency 记录。
--
-- 有两个用例依赖它们，而谁都没造过：
--   * schema36 时代 actor-bound 的 legacy_json 记录，应当被识别并原样重放；
--   * schema36 之前完全不带 actor 的「裸 scope」记录，任何 actor 碰到都必须
--     永久 409，且不允许旁边再写出一条 canonical 行。
-- 没有这些行的时候，两个用例走的是「查无此 key → 正常执行」的路径，
-- 于是断言拿到 200 而不是 201/409，看着像中间件的 legacy 分支坏了，
-- 其实那段代码根本没被碰到。
--
-- 为什么要关掉触发器：00037 的 guard_idempotency_evidence 规定任何 INSERT
-- 都必须以 status='in_flight'、claim_generation=1、未绑定资源的形态开始。
-- 这条规则对活跃流量是对的，但这里要造的恰恰是「旧 schema 时代遗留下来的
-- 终态行」——它们在守卫存在之前就已经落库了。session_replication_role
-- 只停触发器，CHECK 约束照常生效，所以行的形状仍然被真实约束把关。
SET session_replication_role = replica;

-- A) actor-bound 的 legacy_json：scope 带 :actor: 后缀，能被 actor 认领并重放。
--    request_hash 必须和中间件算的一致，否则会被判成「同 key 不同请求」而 409。
--    算法：sha256(method || LF || 请求目标（含 query） || LF || body)。
INSERT INTO idempotency_keys (
  tenant_id, scope, idempotency_key, request_hash, actor_id, status,
  response_code, response_format, response_body,
  claim_generation, created_at, completed_at, expires_at
) VALUES (
  '91000000-0000-7000-8000-000000000001',
  'pg18_legacy_actor_replay:actor:' || encode(substring(
    sha256(convert_to('91000000-0000-7000-8000-000000000011', 'UTF8')) from 1 for 12), 'hex'),
  'legacy-actor-replay-key',
  sha256(convert_to(
    'POST' || chr(10) || '/pg18/legacy-actor?variant=1' || chr(10) || '{"legacy":"request"}',
    'UTF8')),
  '91000000-0000-7000-8000-000000000011',
  'succeeded', 201, 'legacy_json', '{"legacy":true,"source":"schema36"}'::jsonb,
  1, now() - interval '90 days', now() - interval '90 days', now() + interval '90 days'
);

-- B) 裸 scope 的四种遗留状态。actor_id 为 NULL 正是「schema36 之前还没有这一列」
--    的样子，也是 lookup_legacy_idempotency_key 判定 legacy 的依据。
--    只造在租户 A：租户 B 用同一个 key 必须畅通无阻，那是隔离断言的另一半。
INSERT INTO idempotency_keys (
  tenant_id, scope, idempotency_key, request_hash, actor_id, status,
  response_code, response_format, response_body, locked_until,
  claim_generation, created_at, completed_at, expires_at
) VALUES
  -- 远古的在途记录：租约早就过期了，但没有 actor 就无从判断归属，只能拒绝。
  ('91000000-0000-7000-8000-000000000001', 'pg18_legacy_probe',
   'legacy-probe-ancient-inflight',
   sha256(convert_to('POST' || chr(10) || '/pg18/legacy?key=legacy-probe-ancient-inflight'
                     || chr(10), 'UTF8')),
   NULL, 'in_flight', NULL, 'none', NULL, now() - interval '399 days',
   1, now() - interval '400 days', NULL, now() - interval '370 days'),
  -- 新近的在途记录：租约还没到期，更不能放行。
  ('91000000-0000-7000-8000-000000000001', 'pg18_legacy_probe',
   'legacy-probe-recent-inflight',
   sha256(convert_to('POST' || chr(10) || '/pg18/legacy?key=legacy-probe-recent-inflight'
                     || chr(10), 'UTF8')),
   NULL, 'in_flight', NULL, 'none', NULL, now() + interval '5 minutes',
   1, now() - interval '1 hour', NULL, now() + interval '29 days'),
  -- 远古的终态记录：有响应可重放，但不知道该放给谁，同样拒绝。
  ('91000000-0000-7000-8000-000000000001', 'pg18_legacy_probe',
   'legacy-probe-ancient-terminal',
   sha256(convert_to('POST' || chr(10) || '/pg18/legacy?key=legacy-probe-ancient-terminal'
                     || chr(10), 'UTF8')),
   NULL, 'succeeded', 200, 'legacy_json', '{"legacy":"ancient"}'::jsonb, NULL,
   1, now() - interval '400 days', now() - interval '400 days', now() - interval '370 days'),
  -- 新近的终态记录。
  ('91000000-0000-7000-8000-000000000001', 'pg18_legacy_probe',
   'legacy-probe-recent-terminal',
   sha256(convert_to('POST' || chr(10) || '/pg18/legacy?key=legacy-probe-recent-terminal'
                     || chr(10), 'UTF8')),
   NULL, 'succeeded', 200, 'legacy_json', '{"legacy":"recent"}'::jsonb, NULL,
   1, now() - interval '1 hour', now() - interval '1 hour', now() + interval '29 days');

SET session_replication_role = origin;

-- 收口：造出来的东西必须真的是 legacy，否则用例会以「路径没走到」的方式假通过。
DO $$
DECLARE bound int; raw int;
BEGIN
  SELECT count(*) INTO bound FROM idempotency_keys
   WHERE scope LIKE 'pg18_legacy_actor_replay:actor:%' AND actor_id IS NOT NULL
     AND response_format = 'legacy_json';
  SELECT count(*) INTO raw FROM idempotency_keys
   WHERE scope = 'pg18_legacy_probe' AND actor_id IS NULL AND scope !~ ':actor:';
  IF bound <> 1 OR raw <> 4 THEN
    RAISE EXCEPTION 'legacy fixture 不完整: actor-bound=% raw=%', bound, raw;
  END IF;
END $$;
