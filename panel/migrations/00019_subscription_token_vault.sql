-- +goose Up
-- 订阅 token 的可还原存储
--
-- 原本只存 SHA-256。那对密码是对的，但订阅 token 不是密码：
-- 它必须能展示给用户（复制到客户端、换手机时再复制一次），
-- 而单向哈希意味着签发那一刻之后谁也读不回来，面板上什么都显示不了。
--
-- 解法是在哈希之外再存一份信封加密的密文：
--   token_hash       验证用。每次请求都要比对，走索引，快
--   token_encrypted  展示用。要主密钥才能打开，主密钥不在库里
--
-- 这样「拖库」这个最常见的泄露场景下，攻击者拿到的是一列随机字节；
-- 而主密钥用的是已有的 MasterKey（易支付商户密钥也用它），
-- 不引入任何新的密钥管理负担。
ALTER TABLE subscription_credentials
  ADD COLUMN IF NOT EXISTS token_encrypted bytea;

COMMENT ON COLUMN subscription_credentials.token_encrypted IS
  '信封加密后的 token 原文，供面板展示。主密钥不在数据库内；备份时切勿与配置文件打包在一起。';
