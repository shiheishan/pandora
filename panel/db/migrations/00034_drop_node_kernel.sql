-- +goose Up
-- 删 nodes.kernel（产品 2026-10-10 定：不再支持第三方节点端，节点端只有 NativeCore）。属 node 模块。
--
-- 这一列原本给节点端兼容构建选数据面内核（auto / pandora-native，更早还有 sing-box / xray-core），
-- 兼容层已整块删除，节点端从不读它；面板的读写、配置文档与后台接口在同一版本里一起去掉。
-- 还没有生产部署，代码与迁移同版本上线，不走「先停写、下一版再删列」的两步。
-- 锁与耗时（整个迁移在一个事务里，锁持到提交）：DROP COLUMN 只改目录、不重写表，
--   要 nodes 的 ACCESS EXCLUSIVE 锁，拿到后瞬时完成；节点表很小。
--   预检：无（列上没有索引、外键、视图或函数依赖，只有一条列级 CHECK，随列删除）。
--   失败回滚：任一步失败（含 lock_timeout 超时）整笔回滚，库保持原样，换低峰重试即可。
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

ALTER TABLE nodes DROP COLUMN kernel;

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '2min';

-- 按 00018_node.sql 的原定义加回（列级 CHECK 的自动名同为 nodes_kernel_check）；常量缺省值不重写表，
-- 存量行一律取 'auto'。列加在表尾，位置与 00018 建表时不同，不影响按列名读写。
ALTER TABLE nodes
  ADD COLUMN kernel text NOT NULL DEFAULT 'auto' CHECK (kernel IN ('auto', 'pandora-native'));
