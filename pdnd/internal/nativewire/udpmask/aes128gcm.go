package udpmask

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"net"
	"sync"
)

// AES-128-GCM 掩码。
//
// 线格式（和上游 finalmask/mkcp/aes128gcm 一致，否则互通不了）：
//
//	[nonce 12B][AES-128-GCM 密文][tag 16B]
//
// 密钥是 sha256(password) 的前 16 字节。用 sha256 而不是直接取密码，
// 是为了让任意长度、任意字符集的密码都能得到一个定长密钥；截前 16 字节
// 是因为 AES-128 只要 128 位。
//
// # 这层给了什么
//
// 包体完全变成随机字节，没有任何固定结构可供匹配——mKCP 那个「会话号
// 恒定、命令字只有四种取值」的特征被盖住了。同时它是 AEAD：改一个比特
// 就解不开，主动探测发一个构造好的包过来，我们静默丢弃，不回任何东西。
//
// # 这层没给什么
//
// 包长和收发时序没有变。真正下功夫的分析仍然能从「等长小包 + 固定间隔
// 心跳」这类模式上看出端倪。掩码只解决内容可识别，不解决流量画像。
type aes128gcm struct {
	aead cipher.AEAD
}

// NewAES128GCM 用给定密码构造一层 AES-128-GCM 掩码。
func NewAES128GCM(password string) (Mask, error) {
	if password == "" {
		// 空密码等于把密钥固定成 sha256("")，谁都能算出来，那这层
		// 就只是个障眼法而不是加密。宁可不让它启用。
		return nil, errors.New("udpmask: aes128gcm 需要非空密码")
	}
	key := sha256.Sum256([]byte(password))
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &aes128gcm{aead: aead}, nil
}

func (m *aes128gcm) Name() string { return "mkcp-aes128gcm" }

func (m *aes128gcm) Overhead() int { return m.aead.NonceSize() + m.aead.Overhead() }

func (m *aes128gcm) Wrap(pc net.PacketConn) net.PacketConn {
	return &aes128gcmConn{PacketConn: pc, aead: m.aead}
}

type aes128gcmConn struct {
	net.PacketConn
	aead cipher.AEAD

	// 写缓冲要加锁：PacketConn 允许多 goroutine 并发写，而 mKCP 的
	// 后台循环和上层的 Write 确实会同时发包。
	writeMu  sync.Mutex
	writeBuf [MaxPacketSize]byte
}

func (c *aes128gcmConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	nonceSize, overhead := c.aead.NonceSize(), c.aead.Overhead()
	if nonceSize+len(p)+overhead > MaxPacketSize {
		return 0, ErrPacketTooBig
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// 每个包一个全新随机 nonce。GCM 在同一密钥下重用 nonce 会直接泄露
	// 明文异或值，还能让攻击者伪造密文——这不是「安全性下降」，是彻底
	// 失效，所以这里绝不能用计数器省那 12 字节。
	nonce := c.writeBuf[:nonceSize]
	if _, err := rand.Read(nonce); err != nil {
		return 0, err
	}
	sealed := c.aead.Seal(c.writeBuf[nonceSize:nonceSize], nonce, p, nil)

	if _, err := c.PacketConn.WriteTo(c.writeBuf[:nonceSize+len(sealed)], addr); err != nil {
		return 0, err
	}
	// 对上层报告写了多少明文，不是线上多少字节——上层按明文长度记账。
	return len(p), nil
}

func (c *aes128gcmConn) ReadFrom(p []byte) (int, net.Addr, error) {
	nonceSize, overhead := c.aead.NonceSize(), c.aead.Overhead()

	// 原地解：GCM 的 Open 允许 dst 和 ciphertext 重叠，所以不用第二个
	// 缓冲区，也就不用为每个收到的包分配内存。
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return 0, addr, err
		}
		if n < nonceSize+overhead {
			// 太短，连头都装不下。可能是扫描探测，也可能是别的协议打错
			// 端口。丢掉继续读，不往上报错——报上去会让 mKCP 的收包循环
			// 因为一个野包就退出。
			continue
		}
		nonce := p[:nonceSize]
		ciphertext := p[nonceSize:n]
		plain, err := c.aead.Open(ciphertext[:0], nonce, ciphertext, nil)
		if err != nil {
			// 解不开。伪造的包、密码不一致的对端、或者纯粹的噪声，
			// 一律静默丢弃——回任何东西都等于告诉探测方这里有服务。
			continue
		}
		// 明文被解到了 p[nonceSize:]，挪到开头，让上层像读裸 UDP 一样用。
		copy(p, plain)
		return len(plain), addr, nil
	}
}
