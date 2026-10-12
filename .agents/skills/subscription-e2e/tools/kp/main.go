// kp：REALITY（X25519）密钥工具，只用标准库。
//
//	go run ./kp            生成一对新的私钥 / 公钥（base64url 无填充，与 Xray 的 x25519 输出同格式）
//	go run ./kp <私钥>     由私钥算出公钥，核对夹具里的密钥对是否成对
//
// 生成的密钥只用于测试夹具；写进仓库前要登记进 .gitleaks.toml 的放行清单（按完整值）。
package main

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

func main() {
	enc := base64.RawURLEncoding
	var priv *ecdh.PrivateKey
	var err error
	if len(os.Args) > 1 {
		raw, derr := enc.DecodeString(os.Args[1])
		if derr != nil {
			fmt.Fprintln(os.Stderr, "私钥不是 base64url:", derr)
			os.Exit(1)
		}
		priv, err = ecdh.X25519().NewPrivateKey(raw)
	} else {
		// 与 xray x25519 一样先按 RFC 7748 夹紧再输出；公钥与不夹紧时相同
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		raw[0] &= 248
		raw[31] &= 127
		raw[31] |= 64
		priv, err = ecdh.X25519().NewPrivateKey(raw)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("private:", enc.EncodeToString(priv.Bytes()))
	fmt.Println("public: ", enc.EncodeToString(priv.PublicKey().Bytes()))
}
