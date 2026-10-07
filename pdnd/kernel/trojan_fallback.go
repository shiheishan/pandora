package kernel

import (
	"errors"
	"fmt"
	"io"
)

// Trojan 认证判定之前的抗探测部分。
//
// Trojan 请求以 56 个十六进制字符（SHA-224 口令）加 CRLF 开头。原来的读法是
// 先 ReadFull 56 字节再比对：一个 30 字节的 HTTP GET 会被一直挂到 10 秒读超时，
// 一个 60 字节的会在 0 秒被断开，两种反应都不是 Web 服务器会有的。现在逐段
// 校验：一出现不可能属于口令的字节就判定失败，交给回落或中性页面
// （serveProbeFallback）——与 trojan-gfw / trojan-go 「首包不像 Trojan 就转给
// 回落」一致。

const trojanProofLen = 56 + 2

var (
	// errTrojanProofMalformed：首包不可能是 Trojan 口令（非小写十六进制或缺 CRLF）。
	errTrojanProofMalformed = errors.New("trojan password proof malformed")
	// errTrojanUserRejected：口令格式对，但不是任何在册用户。
	errTrojanUserRejected = errors.New("trojan user proof rejected")
)

// readTrojanProof 读满 58 字节口令行，边读边校验；格式不对立刻返回
// errTrojanProofMalformed，不再等凑满。读错误（超时、EOF）原样包一层返回。
func readTrojanProof(r io.Reader, proof *[trojanProofLen]byte) error {
	got := 0
	for got < trojanProofLen {
		n, err := r.Read(proof[got:])
		for i := got; i < got+n; i++ {
			if !trojanProofByteValid(i, proof[i]) {
				return markConnError(connErrProtocol, errTrojanProofMalformed)
			}
		}
		got += n
		if got == trojanProofLen {
			return nil
		}
		if err != nil {
			if err == io.EOF && got > 0 {
				err = io.ErrUnexpectedEOF
			}
			return fmt.Errorf("trojan password proof: %w", err)
		}
	}
	return nil
}

func trojanProofByteValid(index int, b byte) bool {
	switch {
	case index < 56:
		return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
	case index == 56:
		return b == '\r'
	default:
		return b == '\n'
	}
}

// trojanPreAuthRecorder 记下认证判定之前读走的字节（最多 58 字节），认证
// 失败时原样补发给回落目标。认证通过后不再记录。
type trojanPreAuthRecorder struct {
	r      io.Reader
	buf    []byte
	authed bool
}

func (t *trojanPreAuthRecorder) Read(p []byte) (int, error) {
	n, err := t.r.Read(p)
	if !t.authed && n > 0 {
		t.buf = append(t.buf, p[:n]...)
	}
	return n, err
}

func (t *trojanPreAuthRecorder) authenticated() {
	t.authed = true
	t.buf = nil
}

// rejected 报告 err 是否是「认证判定为失败」（而不是读超时、对端断开或认证
// 之后的协议错误）：只有这种才交给回落。
func (t *trojanPreAuthRecorder) rejected(err error) bool {
	if t.authed {
		return false
	}
	return errors.Is(err, errTrojanProofMalformed) || errors.Is(err, errTrojanUserRejected)
}
