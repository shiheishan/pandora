-- Webhook 投递耗时（M7）。
--
-- 投递记录里原本只有状态码和错误信息，看不出对方慢不慢——超时前的 4.9 秒
-- 和 50 毫秒在列表里长得一样。记最后一次尝试的往返耗时（毫秒），没发出请求
-- （地址校验失败、连不上之前就出错）时留空。

-- +goose Up
ALTER TABLE plugin_hook_deliveries
  ADD COLUMN last_duration_ms int NULL
    CONSTRAINT plugin_hook_deliveries_last_duration_ms_check CHECK (last_duration_ms >= 0);

-- +goose Down
ALTER TABLE plugin_hook_deliveries DROP COLUMN IF EXISTS last_duration_ms;
