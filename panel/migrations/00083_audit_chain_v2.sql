-- 审计哈希链第二版口径：链序号 chain_seq。
--
-- 第一版的两个缺陷（都只在校验时暴露，写入一直「成功」）：
--
--   1. 摘要复算不出来。写入时对 json.Marshal 的原始字节求哈希，存进 jsonb；
--      jsonb 会重排键、改空白，校验时读回的字节和写入时不同，任何带摘要的
--      记录都验不过。
--   2. 链的顺序靠 occurred_at。它的默认值 now() 是事务开始时刻，而写入按
--      advisory lock 的先后串行：先开事务、后拿到锁的一方，occurred_at 反而
--      更早。校验按 occurred_at 排序就会在正常并发下报断链，写入端按
--      occurred_at 取链尾还会让两条记录挂到同一个前驱上（链分叉）。
--
-- 第二版由 audit.Write 在同租户的 advisory lock 内取「最大 chain_seq + 1」，
-- 链序号本身参与哈希；摘要先规范化再参与哈希（与 jsonb 的存储形态无关）。
-- chain_seq 非空即第二版口径，(tenant_id, chain_seq) 唯一，链分叉在写入时
-- 就被拒绝。
--
-- 存量记录不改写：本表是追加写，新列对存量行取 NULL（加列不触发 UPDATE、
-- 不改动任何已存值）。chain_seq 为空的行按第一版口径校验，规则见
-- platform/audit 的 VerifyChain：能复算的严格复算；带摘要而复算不出的只核对
-- 链接关系（prev_hash 等于上一条的 entry_hash），并在校验报告里计数。

-- +goose Up
ALTER TABLE audit_events
  ADD COLUMN chain_seq bigint NULL
    CONSTRAINT audit_events_chain_seq_check CHECK (chain_seq > 0);

-- 写入端取链尾走这个索引；唯一约束同时拒绝链分叉
CREATE UNIQUE INDEX audit_events_chain_seq_key
  ON audit_events (tenant_id, chain_seq)
  WHERE chain_seq IS NOT NULL;

COMMENT ON COLUMN audit_events.chain_seq IS
  '审计哈希链第二版口径的租户内序号（从 1 起连续）；NULL 为 00083 之前的第一版记录。';

-- +goose Down
-- 回滚后第一版代码按 occurred_at 取链尾、按旧口径复算，第二版记录一律验不过；
-- 数据不丢，但回滚后写入的记录会接在按时间排序的链尾之后。
DROP INDEX IF EXISTS audit_events_chain_seq_key;
ALTER TABLE audit_events DROP COLUMN IF EXISTS chain_seq;
