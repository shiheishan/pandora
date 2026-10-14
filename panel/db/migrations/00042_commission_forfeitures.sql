-- +goose Up
-- commission 模块：账号注销时作废可提现佣金的记录表（用户 2026-10-14 定：注销时名下未打款的佣金与在途提现都作废）。属 commission 模块。
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
SET LOCAL lock_timeout = '5s';

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
SET LOCAL lock_timeout = '5s';

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
