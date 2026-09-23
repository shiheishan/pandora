-- +goose Up

-- 区分「工单被关闭」的两种原因（对标 Xboard ticket/withdraw）。
--
-- 用户点「撤回」和点「关闭」是两件事：
--   关闭 —— 问题解决了，客服的工作有效
--   撤回 —— 我不需要了，当我没提过
--
-- 都算进 closed 的话，客服的解决率就是假的：用户自己撤掉的工单
-- 会被记成一次成功服务。
--
-- 没有新增 withdrawn 状态，而是加一列原因。理由：状态机被前端筛选、
-- SLA 扫描、列表查询等十几处引用，多一个状态就要逐个补分支，
-- 漏一处就是「撤回的工单在某个列表里消失了」。而「关闭」这个状态本身
-- 对撤回是成立的 —— 变的只是为什么关。

ALTER TABLE public.tickets
  ADD COLUMN closed_reason text
    CHECK (closed_reason IS NULL
        OR closed_reason IN ('user_closed', 'withdrawn', 'agent_closed')),
  ADD COLUMN closed_note text
    CHECK (closed_note IS NULL OR length(closed_note) <= 500);

-- 已有的关闭工单一律标成 user_closed：它们是在撤回功能存在之前关的，
-- 不可能是撤回。留成 NULL 反而会让统计里多出一类「原因不明」。
UPDATE public.tickets SET closed_reason = 'user_closed'
 WHERE status = 'closed' AND closed_reason IS NULL;

-- 按原因查关闭工单是统计的主要用法（解决率、撤回率）。
CREATE INDEX idx_tickets_closed_reason
  ON public.tickets (tenant_id, closed_reason, closed_at DESC)
  WHERE closed_reason IS NOT NULL;

-- +goose Down

DROP INDEX IF EXISTS idx_tickets_closed_reason;
ALTER TABLE public.tickets
  DROP COLUMN IF EXISTS closed_note,
  DROP COLUMN IF EXISTS closed_reason;
