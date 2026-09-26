# panel/internal/platform/crypto/
> L2 | 父级: /panel/internal/platform/CLAUDE.md

口令哈希、令牌、签名与信封加密的唯一实现。一条贯穿全包的原则：会落库的秘密只落哈希或密文。主密钥只做两件事：信封加密的根，以及派生用途专用的盐——每个用途一个域分隔串（aegis/<用途>/…/v1），盐之间、盐与主密钥之间互不相通，一张表的哈希泄露比对不了另一张表。派生公式一旦落了存量哈希就不能改，由单测钉死。

成员清单
crypto.go: Argon2id 口令哈希与计时对齐的 DummyVerify；随机令牌与数字验证码；HashIdentifier（规范化后 HMAC）与 HashRaw（原样 HMAC）；Ed25519 Signer；AES-GCM 信封 Envelope；SubscriptionAuditSalt（订阅审计 IP/UA 哈希）与 NotifyRecipientSalt（通知收件人哈希，admin 与 public 两个网关共用）
password_policy.go: ValidatePassword 产品口令规则：至少 8 位、同时含字母与数字，上限防 Argon2 输入过大
*_test.go: 口令规则边界；派生盐的公式、确定性与互不相同

法则: 成员完整·一行一文件·父级链接·技术词前置
[PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md
