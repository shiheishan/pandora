-- +goose Up
-- 支付成功通知：notify 扫描按 (fulfilled_at, id) 游标经 order 读已履约、挂在订阅上的订单（充值单没有订阅），
-- 写完一页再推进游标，停机多久都不漏。还没有生产部署，索引直接建；以后有大表时改成 CREATE INDEX CONCURRENTLY
-- 并加 NO TRANSACTION。
CREATE INDEX orders_fulfilled_idx ON orders (fulfilled_at, id)
  WHERE status = 'fulfilled' AND subscription_id IS NOT NULL;

-- notify 的扫描游标：每种游标一行，记已处理到的 (last_at, last_id)。属 notify 模块。
CREATE TABLE notify_scan_cursors (
  name       text PRIMARY KEY CHECK (name IN ('order_paid')),
  last_at    timestamptz NOT NULL,
  last_id    uuid NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS notify_scan_cursors;
DROP INDEX IF EXISTS orders_fulfilled_idx;
