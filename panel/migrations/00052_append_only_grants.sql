-- +goose Up

-- 把新加的追加写表在授权层也收紧，和项目里既有的做法对齐。
--
-- 起因：00011 里有一句
--     ALTER DEFAULT PRIVILEGES IN SCHEMA public
--       GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO aegis_app;
-- 于是此后每建一张表，aegis_app 都自动拿到全套 DML。后面的迁移里写
--     GRANT SELECT, INSERT ON gift_card_redemptions TO aegis_app;
-- 看起来是在「只给读和写入」，实际上 GRANT 只做加法 —— 那一句是空操作，
-- UPDATE 和 DELETE 早就在了。
--
-- 现在没有实际漏洞：这两张表上的 app.deny_mutation 触发器挡得住，实测
-- DELETE 和 UPDATE 都会被拒。但既有的 audit_events / ledger_entries /
-- referrals 是两层防护 —— 授权层先拒，触发器兜底；这两张表只剩一层。
-- 差别在于：触发器可以被一次误操作的迁移 DROP 掉，而且那不会有任何报错，
-- 表会安静地变成可改的。授权层再挡一道，把「一次失误」变成「两次失误」。
--
-- payment_events 不动：它的证据链触发器给出的报错信息是定制过的
-- （「记录为证据链的一部分」），说明那张表的语义由业务方定义，
-- 这里不替它做决定。

REVOKE UPDATE, DELETE ON gift_card_redemptions FROM aegis_app;
REVOKE UPDATE, DELETE ON traffic_reset_logs   FROM aegis_app;

-- +goose Down

GRANT UPDATE, DELETE ON gift_card_redemptions TO aegis_app;
GRANT UPDATE, DELETE ON traffic_reset_logs   TO aegis_app;
