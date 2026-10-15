-- +goose Up
-- 用户 2026-10-14 定的 Xboard 补齐注册与用户条目；属 identity 模块，另有 subscription 的试用表与事件类型、notify 的一条模板种子、
-- commission 的注销作废记录表（第 8 段）。
--
-- 锁与耗时：users 不是小表。重建 users_status_check、新增 users_deleted_shape 两条 CHECK 都在 ACCESS EXCLUSIVE 锁下
--   各扫一遍 users 全表（加列不扫：admin_note、deleted_at 没有默认值），耗时与行数成正比，期间 users 读写全部排队；
--   subscription_events 重建 CHECK 同样扫一遍全表。其余是小表上的 ALTER / CREATE，毫秒级。锁都持到提交。
--   预检：SELECT count(*) FROM users; SELECT count(*) FROM subscription_events;——百万行以上先在同规模的库上量一次，
--   超过 statement_timeout（2 分钟）就挑低峰、临时调大它再跑；拿不到锁 5 秒即失败（lock_timeout），不会长时间挡住业务。
--   失败回滚：整笔回滚，库保持原样，可以直接重跑。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 1. users：状态增加 deleted（注销并匿名化后的账号，不能登录、不能找回）；增加后台备注 admin_note 与注销时间 deleted_at
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'suspended', 'banned', 'deleted'));
ALTER TABLE users ADD COLUMN admin_note text CHECK (char_length(admin_note) <= 2000), ADD COLUMN deleted_at timestamptz;
ALTER TABLE users ADD CONSTRAINT users_deleted_shape CHECK ((status = 'deleted') = (deleted_at IS NOT NULL));

-- 2. invite_codes：允许多个有效码（由 identity service 锁用户行后按设置计数）；增加访问量 visit_count（按 IP 去重计数，不存 IP）
DROP INDEX invite_codes_one_active_per_owner;
CREATE INDEX invite_codes_owner_idx ON invite_codes (owner_user_id, created_at DESC) WHERE owner_user_id IS NOT NULL;
ALTER TABLE invite_codes ADD COLUMN visit_count bigint NOT NULL DEFAULT 0 CHECK (visit_count >= 0);

-- 3. referral_changes（属主 identity，只追加）：后台改邀请人的记录
CREATE TABLE referral_changes (
  id                        uuid PRIMARY KEY DEFAULT uuidv7(),
  referee_user_id           uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  -- 改成谁；空 = 后台清除了邀请人
  referrer_user_id          uuid REFERENCES users(id) ON DELETE RESTRICT,
  -- 改之前生效的邀请人（审计对照用）
  previous_referrer_user_id uuid REFERENCES users(id) ON DELETE RESTRICT,
  -- 操作的管理员；不挂外键，与审计的 actor_id 一样
  changed_by                uuid NOT NULL,
  reason                    text NOT NULL CHECK (char_length(reason) BETWEEN 5 AND 500),
  created_at                timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT referral_changes_no_self CHECK (referrer_user_id IS NULL OR referrer_user_id <> referee_user_id)
);
CREATE INDEX referral_changes_referee_idx ON referral_changes (referee_user_id, created_at DESC, id DESC);
CREATE INDEX referral_changes_referrer_idx ON referral_changes (referrer_user_id) WHERE referrer_user_id IS NOT NULL;
CREATE TRIGGER referral_changes_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON referral_changes
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

COMMENT ON TABLE referral_changes IS '后台改邀请人的记录（只追加）。referrals 是注册时的原始绑定，不可改（MKT-003）；某人有改动记录时，以最新一条为准（见视图 effective_referrals）。只影响以后的订单计佣。';

-- 4. 视图 effective_referrals（属主 identity）：每个被邀请人当前生效的邀请人
CREATE VIEW effective_referrals AS
SELECT r.referee_user_id, r.referrer_user_id, r.invite_code_id, r.risk_flag, r.bound_at
  FROM referrals r
 WHERE NOT EXISTS (SELECT 1 FROM referral_changes c WHERE c.referee_user_id = r.referee_user_id)
UNION ALL
-- 改过邀请人的沿用原绑定的风险标记（原绑定是 suspicious / confirmed_fraud 的，后台改了邀请人也不该把标记洗掉）；
-- 注册时没有绑定、后台直接设的，记 none。
SELECT l.referee_user_id, l.referrer_user_id, NULL::uuid AS invite_code_id,
       COALESCE(o.risk_flag, 'none'::text) AS risk_flag, l.created_at AS bound_at
  FROM (SELECT DISTINCT ON (referee_user_id) referee_user_id, referrer_user_id, created_at
          FROM referral_changes
         ORDER BY referee_user_id, created_at DESC, id DESC) l
  LEFT JOIN referrals o ON o.referee_user_id = l.referee_user_id
 WHERE l.referrer_user_id IS NOT NULL;

COMMENT ON VIEW effective_referrals IS '每个被邀请人当前生效的邀请人；读当前邀请人（计佣、详情、邀请人数）一律读它。';

-- 5. subscription_trials（属主 subscription，只追加）：注册试用发放记录
CREATE TABLE subscription_trials (
  -- 主键 = 每人终身只发一次注册试用（SUB-007）
  user_id         uuid PRIMARY KEY REFERENCES users(id) ON DELETE RESTRICT,
  subscription_id uuid NOT NULL REFERENCES subscriptions(id) ON DELETE RESTRICT,
  plan_id         uuid NOT NULL REFERENCES plans(id) ON DELETE RESTRICT,
  hours           int NOT NULL CHECK (hours BETWEEN 1 AND 8760),
  granted_at      timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT subscription_trials_subscription_key UNIQUE (subscription_id)
);
CREATE TRIGGER subscription_trials_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON subscription_trials
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

COMMENT ON TABLE subscription_trials IS '注册送试用的发放记录；试用不建订单，所以不计佣、不进收入。';

-- 6. notify 模板种子（属主 notify）：免密登录链接模板
INSERT INTO notification_templates (code, channel, subject, body) VALUES
('auth.login_link', 'email', '【{{site}}】登录链接',
'你好，

点击下面的链接登录 {{site}}：

{{link}}

链接 {{minutes}} 分钟内有效，只能用一次。如果这不是你本人的操作，忽略这封邮件即可，你的账号不会有任何变化。

{{site}}');

-- 7. subscription_events（属主 subscription）：后台改到期、改配额上限也记进订阅时间线
ALTER TABLE subscription_events DROP CONSTRAINT subscription_events_event_type_check;
ALTER TABLE subscription_events ADD CONSTRAINT subscription_events_event_type_check
  CHECK (event_type IN ('activated', 'renewed', 'extended', 'plan_changed', 'expired', 'renewal_closed', 'link_rotated',
                        'period_end_set', 'quota_limit_set'));

-- 8. commission_forfeitures（属 commission 模块，随注销功能放在本迁移里）：账号注销时作废可提现佣金的记录表
--    （用户 2026-10-14 定：注销时名下未打款的佣金作废、申请中的提现驳回；有已批准未打款的提现时不许注销）。
--
-- 为什么另起一张表：可提现佣金是个「池」，不按条目扣——提现、转余额都按 `已解冻 − 提现 − 转余额` 的口径从池里出，
-- 说不清哪条已解冻的条目被提走了多少，所以没法把「剩下的那部分」落到某几条条目上作废。注销时按唯一口径算出每个币种还剩的
-- 可用佣金，记一笔 commission_voided（借 可提现佣金 / 贷 平台收入 main），在这里记一行（id 即这笔交易的来源 id），
-- 可用佣金口径相应改成再减去这里的合计。冻结中、待复核的条目仍按条目作废（status → voided），不进这张表。
--
-- 锁与耗时：只建新表、建索引、挂触发器，不碰已有表的数据；外键要在 users、ledger_transactions 上拿 SHARE ROW EXCLUSIVE，
-- 只挡这两张表的写入、不挡读，持续到本迁移提交（毫秒级）。
-- 预检：无（新表）。
-- 失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，重试即可。

CREATE TABLE commission_forfeitures (
  -- 与账本 commission_voided 交易的来源 id 相同（source_type = 'commission_forfeiture'）
  id            uuid PRIMARY KEY,
  user_id       uuid NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  currency      text NOT NULL CHECK (currency IN ('CNY', 'USD')),
  amount        numeric(20,2) NOT NULL CHECK (amount > 0),
  -- 目前只有注销一种原因；以后加原因要同时改 Go 的常量
  reason        text NOT NULL CHECK (reason IN ('account_deleted')),
  ledger_txn_id uuid NOT NULL,
  created_at    timestamptz NOT NULL DEFAULT now(),
  CONSTRAINT commission_forfeitures_ledger_txn_key UNIQUE (ledger_txn_id),
  -- 交易外键带上币种（同 commission_transfers）：记录币种必须等于账本交易币种
  CONSTRAINT commission_forfeitures_ledger_txn_fk FOREIGN KEY (ledger_txn_id, currency)
    REFERENCES ledger_transactions (id, currency) ON DELETE RESTRICT,
  -- 注销只有一次：同一个人同一币种最多作废一次（重放、并发的第二次撞这里）
  CONSTRAINT commission_forfeitures_user_currency_key UNIQUE (user_id, currency)
);

CREATE TRIGGER commission_forfeitures_append_only
  BEFORE UPDATE OR DELETE OR TRUNCATE ON commission_forfeitures
  FOR EACH STATEMENT EXECUTE FUNCTION deny_mutation();

-- +goose Down
-- 数据有损，回退前想清楚：
--   referral_changes 整表删掉，所有「后台改过的邀请人」随之消失，每个人的当前邀请人悄悄回到注册时的原始绑定，
--   以后订单的计佣归属跟着变（已记的佣金不动）；users 的 admin_note、deleted_at 两列删掉，已注销账号改记成 banned；
--   subscription_trials 删掉后，领过注册试用的人重新升级上来会被当成没领过（试用订阅本身还在）。
-- 锁与耗时同 Up：users 的 CHECK 重建扫一遍全表，subscription_events 的 CHECK 用 NOT VALID 不扫。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 先回退 commission_forfeitures（Up 里最后建的）
-- 已有作废记录时拒绝回退：删表会让账本里 source_type = 'commission_forfeiture' 的冲回交易失去来历，
-- 可用佣金口径也会把已作废的钱重新算回来
-- +goose StatementBegin
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM commission_forfeitures) THEN
    RAISE EXCEPTION 'commission_forfeitures 已有注销作废记录，不能回退本迁移（先人工处理）';
  END IF;
END
$$;
-- +goose StatementEnd

-- 触发器、索引、约束随表删除
DROP TABLE commission_forfeitures;


-- 0. 订阅事件：只追加的表里已有的 period_end_set / quota_limit_set 行删不掉，加回旧 CHECK 时用 NOT VALID，只约束之后的写入
ALTER TABLE subscription_events DROP CONSTRAINT subscription_events_event_type_check;
ALTER TABLE subscription_events ADD CONSTRAINT subscription_events_event_type_check
  CHECK (event_type IN ('activated', 'renewed', 'extended', 'plan_changed', 'expired', 'renewal_closed', 'link_rotated')) NOT VALID;

-- 1. notify 模板种子
DELETE FROM notification_templates WHERE code = 'auth.login_link' AND channel = 'email';

-- 2. 试用表
DROP TABLE IF EXISTS subscription_trials;

-- 3. 生效邀请人视图
DROP VIEW IF EXISTS effective_referrals;

-- 4. 邀请人变更记录表
DROP TABLE IF EXISTS referral_changes;

-- 5. invite_codes：退回后一人只能有一个有效码，保留最早的那个
UPDATE invite_codes SET status = 'disabled'
 WHERE status = 'active' AND owner_user_id IS NOT NULL
   AND id NOT IN (SELECT DISTINCT ON (owner_user_id) id FROM invite_codes
                   WHERE status = 'active' AND owner_user_id IS NOT NULL
                   ORDER BY owner_user_id, created_at, id);
ALTER TABLE invite_codes DROP COLUMN IF EXISTS visit_count;
DROP INDEX IF EXISTS invite_codes_owner_idx;
CREATE UNIQUE INDEX invite_codes_one_active_per_owner ON invite_codes (owner_user_id)
  WHERE status = 'active' AND owner_user_id IS NOT NULL;

-- 6. users：已注销账号退回后改记成封禁（旧结构没有 deleted；banned 同样不能登录，邮箱已是占位值，不会被找回）
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_deleted_shape;
UPDATE users SET status = 'banned' WHERE status = 'deleted';
ALTER TABLE users DROP COLUMN IF EXISTS deleted_at, DROP COLUMN IF EXISTS admin_note;
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'suspended', 'banned'));
