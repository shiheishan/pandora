-- +goose Up
-- 把 pandora-native 加进节点内核的取值集合。
--
-- 应用层早就允许选它了（protocol_schema 的 kernel 校验里有），但数据库
-- 这一侧的 CHECK 还停在 00016 定的三个值上。结果是后台能选、一保存就
-- 500——写入被 nodes_kernel_check 拒掉，用户只看得到「服务暂时不可用」。
-- 这是端到端拉通时才暴露的：两层各自都「支持」了，中间那道约束没人动。
--
-- pandora-native 是自研内核 Pandora NativeCore。它和 sing-box / xray-core
-- 一样是显式指定项，不参与 auto 的自动选择——auto 的语义是「节点端按协议
-- 挑一个成熟实现」，把自研内核塞进去会让默认行为随版本漂移。

ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_kernel_check;
ALTER TABLE nodes
  ADD CONSTRAINT nodes_kernel_check
  CHECK (kernel IN ('auto', 'sing-box', 'xray-core', 'pandora-native'));

COMMENT ON COLUMN nodes.kernel IS
  '承载该节点的内核：auto=节点端按协议自选，sing-box / xray-core / '
  'pandora-native=强制指定。对 mieru、juicity 这类只有单一实现的协议无效。';

-- +goose Down
-- 回滚前得先把已经指到自研内核的节点挪走，否则约束加不回去。
UPDATE nodes SET kernel = 'auto' WHERE kernel = 'pandora-native';

ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_kernel_check;
ALTER TABLE nodes
  ADD CONSTRAINT nodes_kernel_check
  CHECK (kernel IN ('auto', 'sing-box', 'xray-core'));

COMMENT ON COLUMN nodes.kernel IS
  '承载该节点的内核：auto=节点端按协议自选，sing-box / xray-core=强制指定。'
  '对 mieru、juicity 这类只有单一实现的协议无效。';
