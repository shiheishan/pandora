-- +goose Up
-- 节点内核选择（NODE-011）
--
-- 同一个协议可以由不同内核承载：VLESS 既能跑在 sing-box 上，也能跑在
-- xray-core 上。两者的协议实现有细微差异（XHTTP 传输只有 xray 有，
-- 部分客户端也只认 xray 的实现细节），因此该用哪个是运营决策而非技术定论，
-- 必须能在面板上按节点配置。
--
-- 'auto' 表示交给节点端按协议自动选择。它是默认值，也是绝大多数节点该用的值 ——
-- 只有在确实遇到某个内核的兼容问题时，才需要手动钉死。

ALTER TABLE nodes
  ADD COLUMN IF NOT EXISTS kernel text NOT NULL DEFAULT 'auto';

-- 取值收敛在数据库层。节点端拿到一个不认识的内核名时只能退回 auto，
-- 那等于配置被静默忽略 —— 让写入直接失败要好得多。
ALTER TABLE nodes
  DROP CONSTRAINT IF EXISTS nodes_kernel_check;
ALTER TABLE nodes
  ADD CONSTRAINT nodes_kernel_check
  CHECK (kernel IN ('auto', 'sing-box', 'xray-core'));

COMMENT ON COLUMN nodes.kernel IS
  '承载该节点的内核：auto=节点端按协议自选，sing-box / xray-core=强制指定。'
  '对 mieru、juicity 这类只有单一实现的协议无效。';
