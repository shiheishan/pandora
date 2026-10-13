-- +goose Up
-- 支付成功通知：notify 扫描每 5 分钟经 order 读最近履约、挂在订阅上的订单（按履约时间），这里给它一个部分索引，
-- 免得扫全表。还没有生产部署，直接建；以后有大表时改成 CREATE INDEX CONCURRENTLY 并加 NO TRANSACTION。
CREATE INDEX orders_fulfilled_idx ON orders (fulfilled_at, id)
  WHERE status = 'fulfilled' AND subscription_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS orders_fulfilled_idx;
