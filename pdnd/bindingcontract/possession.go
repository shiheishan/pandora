package bindingcontract

import "strings"

// KeyPossessionPurpose 是持有证明目前唯一的用途：旧的按节点接入升级为服务器绑定。
const KeyPossessionPurpose = "upgrade-to-server"

// KeyPossessionFields 是 POST /v1/nodes/upgrade-to-server 请求体里 possession_signature
// 覆盖的字段。外层请求由旧节点身份按 PANDORA-NODE-REQUEST-V2 签名（证明「我是这个节点」），
// 这一层由新生成的服务器身份私钥签名（证明「新公钥的私钥确实在我手里」），
// 两层都绑定同一个请求 nonce，所以不能拆开重组。
type KeyPossessionFields struct {
	TenantID           string
	NodeID             string // 发起升级的旧节点身份
	RequestNonce       string // 外层请求的 X-Node-Nonce
	ServerPublicKey    string // 新服务器身份 Ed25519 公钥，标准 base64
	ServerEncPublicKey string // 新服务器 X25519 加密公钥，标准 base64
}

// KeyPossessionPreimage 返回新服务器身份私钥签名、面板用请求体里的 server_public_key 验签的原像。
func KeyPossessionPreimage(in KeyPossessionFields) ([]byte, error) {
	if err := checkUUID("tenant_id", in.TenantID); err != nil {
		return nil, err
	}
	if err := checkUUID("node_id", in.NodeID); err != nil {
		return nil, err
	}
	if err := checkNonce("request_nonce", in.RequestNonce); err != nil {
		return nil, err
	}
	if err := checkPublicKey32("server_public_key", in.ServerPublicKey); err != nil {
		return nil, err
	}
	if err := checkPublicKey32("server_enc_public_key", in.ServerEncPublicKey); err != nil {
		return nil, err
	}
	var b strings.Builder
	b.Grow(320)
	b.WriteString(KeyPossessionContract)
	b.WriteByte('\n')
	line(&b, "purpose", KeyPossessionPurpose)
	line(&b, "tenant_id", in.TenantID)
	line(&b, "node_id", in.NodeID)
	line(&b, "request_nonce", in.RequestNonce)
	line(&b, "server_public_key", in.ServerPublicKey)
	line(&b, "enc_kem", EncKEM)
	line(&b, "server_enc_public_key", in.ServerEncPublicKey)
	return []byte(b.String()), nil
}
