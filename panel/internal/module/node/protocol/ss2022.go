package protocol

// ss2022Methods 是开放的 Shadowsocks 2022 方法（SIP022），按下拉顺序。
// 2022-blake3-chacha20-poly1305 不开放：多用户靠 SIP023 身份头，身份头用 AES 块加密，chacha20 版做不了单端口多用户，
// 客户端（sing-shadowsocks 等）给它多段密钥直接报错。
var ss2022Methods = []string{"2022-blake3-aes-128-gcm", "2022-blake3-aes-256-gcm"}

// ss2022KeyLens 是各 2022 方法的密钥长度（字节）。subscription/render/opts.go 另有一份（纯函数包不能互相 import），
// 加改方法两边一起改；对照测试在 node/service/ss2022_keylen_parity_test.go。
var ss2022KeyLens = map[string]int{"2022-blake3-aes-128-gcm": 16, "2022-blake3-aes-256-gcm": 32}

// SS2022KeyLen 报告 method 是不是开放的 Shadowsocks 2022 方法，是则返回密钥长度。method 是内核名（表单的 cipher 翻译后）。
func SS2022KeyLen(method string) (int, bool) {
	n, ok := ss2022KeyLens[method]
	return n, ok
}
