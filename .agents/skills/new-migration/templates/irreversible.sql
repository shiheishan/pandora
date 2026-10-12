-- irreversible: <为什么 Down 无法还原：如「修数据，修复前的值没有留存，分不清哪些行是这条迁移改的」>
-- forward-fix: <出问题怎么前滚补救；要整体回到本迁移之前，走升级前备份恢复（panel/deploy/MIGRATION-RUNBOOK.md 第 3 节）>
-- 以上两行必须在 `-- +goose Up` 之前：migrate.sh rollback-to 与往返门禁都只认文件头里的 irreversible。
-- 大表上的修数据 / 回填再加一行（同样在文件头）：
-- backfill: batched-by=<分批方式，如 租户 × UTC 自然日>; rerunnable=<为什么重跑安全，如 只往前推、已一致的行不动>
--
-- <一句话：修什么>（<谁定的>；<任务路名>）。
-- <哪些行可以安全改、哪些不碰（已吊销、空值、已用量）；修复只往前推、不缩短>。

-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '10min';

UPDATE <表>
   SET <列> = <新值>, updated_at = now()
 WHERE <只命中需要修的行，重跑一次零改动>;

-- +goose Down
-- irreversible 的 Down 只 RAISE，不要求设超时；不要写成空 Down 或 SELECT 1（会静默「成功」）。
-- +goose StatementBegin
DO $$
BEGIN
  RAISE EXCEPTION
    'rollback refused (<NNNNN> <名字>): <原因>; <前滚办法>, or restore the pre-upgrade backup';
END
$$;
-- +goose StatementEnd
