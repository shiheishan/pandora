package dbbackup

import (
	"crypto/ecdh"
	"errors"
	"strings"
	"unicode"
)

// age 的私钥与收件人都是 Bech32（BIP-173）编码：私钥的人类可读部分是 age-secret-key-（文件里写成大写），
// 收件人是 age。封条前要核两件事：私钥行是不是一把完整、校验和对得上的 X25519 私钥（截断、改了一个字都能看出来）；
// 它推出来的收件人是不是 .env 的 AEGIS_BACKUP_AGE_RECIPIENT（备份就是加密给这个收件人的；对不上，封条有效、
// 备份却解不开）。不依赖外部的 age-keygen

const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

var bech32Generator = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

func bech32Polymod(values []byte) uint32 {
	chk := uint32(1)
	for _, v := range values {
		top := chk >> 25
		chk = (chk&0x1ffffff)<<5 ^ uint32(v)
		for i := 0; i < 5; i++ {
			if (top>>i)&1 == 1 {
				chk ^= bech32Generator[i]
			}
		}
	}
	return chk
}

func bech32HRPExpand(hrp string) []byte {
	out := make([]byte, 0, len(hrp)*2+1)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]>>5)
	}
	out = append(out, 0)
	for i := 0; i < len(hrp); i++ {
		out = append(out, hrp[i]&31)
	}
	return out
}

// convertBits 在 8 位与 5 位分组之间转换；pad=false 时多出的位必须是 0 且不足一组（严格解码）
func convertBits(data []byte, from, to uint, pad bool) ([]byte, bool) {
	var acc, bits uint
	maxv := uint(1)<<to - 1
	var out []byte
	for _, b := range data {
		if uint(b)>>from != 0 {
			return nil, false
		}
		acc = acc<<from | uint(b)
		bits += from
		for bits >= to {
			bits -= to
			out = append(out, byte(acc>>bits&maxv))
		}
	}
	if pad {
		if bits > 0 {
			out = append(out, byte(acc<<(to-bits)&maxv))
		}
	} else if bits >= from || acc<<(to-bits)&maxv != 0 {
		return nil, false
	}
	return out, true
}

// bech32Decode 解出 hrp 与数据（8 位）；大小写混写、字符集外、校验和不对都报错
func bech32Decode(s string) (string, []byte, error) {
	if strings.ToLower(s) != s && strings.ToUpper(s) != s {
		return "", nil, errors.New("大小写混写")
	}
	s = strings.ToLower(s)
	sep := strings.LastIndexByte(s, '1')
	if sep < 1 || sep+7 > len(s) {
		return "", nil, errors.New("格式不对")
	}
	hrp := s[:sep]
	values := make([]byte, 0, len(s)-sep-1)
	for i := sep + 1; i < len(s); i++ {
		k := strings.IndexByte(bech32Charset, s[i])
		if k < 0 {
			return "", nil, errors.New("字符集外的字符")
		}
		values = append(values, byte(k))
	}
	if bech32Polymod(append(bech32HRPExpand(hrp), values...)) != 1 {
		return "", nil, errors.New("校验和不对")
	}
	data, ok := convertBits(values[:len(values)-6], 5, 8, false)
	if !ok {
		return "", nil, errors.New("填充位不对")
	}
	return hrp, data, nil
}

func bech32Encode(hrp string, data []byte) string {
	values, _ := convertBits(data, 8, 5, true)
	poly := bech32Polymod(append(append(bech32HRPExpand(hrp), values...), 0, 0, 0, 0, 0, 0)) ^ 1
	var b strings.Builder
	b.WriteString(hrp)
	b.WriteByte('1')
	for _, v := range values {
		b.WriteByte(bech32Charset[v])
	}
	for i := 0; i < 6; i++ {
		b.WriteByte(bech32Charset[(poly>>(5*(5-i)))&31])
	}
	return b.String()
}

// decodeAgeSecretKey 把（已规范化成大写的）私钥行解成 32 字节 X25519 私钥
func decodeAgeSecretKey(line string) ([]byte, error) {
	hrp, data, err := bech32Decode(line)
	if err != nil || hrp != "age-secret-key-" || len(data) != 32 {
		return nil, errors.New("备份解密私钥不是一把完整的 age 私钥（截断、改了字或校验和不对）")
	}
	return data, nil
}

// ageRecipientOf 算出私钥对应的收件人（age1…）
func ageRecipientOf(secret []byte) (string, error) {
	key, err := ecdh.X25519().NewPrivateKey(secret)
	if err != nil {
		return "", errors.New("备份解密私钥无效")
	}
	return bech32Encode("age", key.PublicKey().Bytes()), nil
}

// ageReadsIdentityFile 按 age 自己读私钥文件的口径核一遍：age 逐行读（只去掉行尾 CR，不去别的空白），
// 跳过空行和以 # 开头的行，其余每行都当一把私钥解析；它的 Bech32 不收大小写混写，人类可读部分又必须恰好是
// 大写的 AGE-SECRET-KEY-。所以除空行和注释外，每行都要整行大写、不含任何空白。封条这边的派生更宽
// （去空白、转大写），这里补上 age 的口径：age 读不了的文件，封条再对也恢复不了
func ageReadsIdentityFile(raw []byte) error {
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSuffix(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if line != strings.ToUpper(line) || strings.IndexFunc(line, unicode.IsSpace) >= 0 {
			return errors.New("age 读不了这个备份解密私钥文件：私钥行要整行大写、前后不能有空白，空行里不能有空格，注释的 # 要顶格")
		}
	}
	return nil
}
