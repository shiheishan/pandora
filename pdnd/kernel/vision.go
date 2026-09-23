package kernel

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"sync"
)

// visionTrace 由环境变量 VISION_TRACE 打开，默认关闭且零成本。
// 排查 Vision 互操作只能看真实字节和真实时序——从字节计数往回推断
// 曾经连着推错过五轮，那些计数包在 net.Conn 外面，根本看不见 TLS
// 记录边界。
var visionTrace = os.Getenv("VISION_TRACE") != ""

func visionTracef(format string, args ...any) {
	if !visionTrace {
		return
	}
	fmt.Fprintf(os.Stderr, "[VISION] "+format+"\n", args...)
}

// XTLS Vision（xtls-rprx-vision）服务端实现。
//
// 逐字节对齐 xray-core 的 proxy/proxy.go：这层是 wire format，第三方客户端
// （v2rayN / sing-box / Clash.Meta）按上游的字节布局收发，差一个字节就是
// 连不上。所以这里的常量、状态机分支、padding 长度公式都照抄上游语义，
// 不做「看起来更合理」的改动。
//
// # Vision 在做什么
//
// VLESS 里跑的通常是 TLS 流量，外面再套一层 TLS（或 REALITY）就成了双重
// 加密：CPU 白烧，而且内层 TLS 记录的长度会原样透出到外层，形成可被统计
// 分析识别的特征。Vision 的做法是——前几个包加随机填充把握手长度糊掉，
// 一旦确认内层是 TLS 1.3（那已经足够安全），就切成裸转发，不再加解密。
//
// # 两个方向
//
// 我们是服务端，只处理 inbound 一侧：
//   - 读（uplink，客户端→我们）：剥 padding，读到 command=2 切直通
//   - 写（downlink，我们→客户端）：加 padding，确认内层 TLS 1.3 后
//     发一个 command=2 然后切直通

// Vision 帧命令。
const (
	visionCommandPaddingContinue byte = 0x00
	visionCommandPaddingEnd      byte = 0x01
	visionCommandPaddingDirect   byte = 0x02
)

// TLS 识别用的字节序列，取值同上游。
var (
	visionTLS13SupportedVersions = []byte{0x00, 0x2b, 0x00, 0x02, 0x03, 0x04}
	visionTLSClientHelloStart    = []byte{0x16, 0x03}
	visionTLSServerHelloStart    = []byte{0x16, 0x03, 0x03}
	visionTLSAppDataStart        = []byte{0x17, 0x03, 0x03}
)

const (
	visionHandshakeTypeServerHello byte = 0x02
	visionHandshakeTypeClientHello byte = 0x01

	// visionBufferSize 是上游 buf.Size。padding 上限、分片阈值都以它为准，
	// 换个数会让分片位置和上游对不上。
	visionBufferSize = 8192
	// visionFrameOverhead：命令头 5 字节 + 首帧 16 字节 UUID。
	visionFrameOverhead = 21

	// 前 8 个包内做 TLS 识别，之后不再看。
	visionPacketsToFilter = 8
)

// padding 长度参数，对应上游默认 testseed {900, 500, 900, 256}。
const (
	visionSeedShortContent = 900 // 内容短于它且允许长填充时走「长填充」分支
	visionSeedLongRandom   = 500
	visionSeedLongBase     = 900
	visionSeedShortRandom  = 256
)

// TLS 1.3 密码套件。TLS_AES_128_CCM_8_SHA256 的认证标签只有 8 字节，
// 上游不对它启用直通，这里跟随。
var visionTLS13Ciphers = map[uint16]string{
	0x1301: "TLS_AES_128_GCM_SHA256",
	0x1302: "TLS_AES_256_GCM_SHA384",
	0x1303: "TLS_CHACHA20_POLY1305_SHA256",
	0x1304: "TLS_AES_128_CCM_SHA256",
	0x1305: "TLS_AES_128_CCM_8_SHA256",
}

// visionState 是一条连接上双向共享的识别结果。
//
// 读方向识别出「内层是 TLS 1.3」之后，写方向才敢切直通，所以这些字段
// 必须是同一份，不能每个方向各存一套。
type visionState struct {
	mu sync.Mutex

	// candidates 是这个入站上全部已授权用户的 UUID。
	//
	// 服务端在解出 VLESS 请求头之前并不知道来的是谁，而 Vision 的首帧
	// 恰恰把 VLESS 头也包在里面——UUID 是唯一能先认出来的东西。所以拿
	// 全量候选去比对帧首 16 字节，命中即锁定。
	candidates [][]byte
	// userUUID 是命中的那一个，命中前为空。
	userUUID []byte
	// probed 表示首帧探测已经做过；notVision 表示探测结论是「这不是
	// Vision 流」。没有这两个标记的话，一条普通 VLESS 连接每次 Read
	// 都会拿流中间的字节去当帧首匹配，迟早撞上一次假命中。
	probed               bool
	notVision            bool
	packetsToFilter      int
	enableXtls           bool
	isTLS                bool
	isTLS12OrAbove       bool
	cipher               uint16
	remainingServerHello int32

	// 读侧状态机
	withinPaddingBuffers bool
	remainingCommand     int32
	remainingContent     int32
	remainingPadding     int32
	currentCommand       int
	readerDirect         bool

	// 写侧状态机
	isPadding    bool
	writerDirect bool
	// writeOnceUUID 只在第一帧里写一次，写完置空。
	writeOnceUUID []byte
}

func newVisionState(candidates [][]byte) *visionState {
	cloned := make([][]byte, 0, len(candidates))
	for _, c := range candidates {
		cloned = append(cloned, append([]byte(nil), c...))
	}
	// 回程首帧也带 UUID，取值就是客户端发来的那个。
	//
	// 上游 NewVisionWriter 无条件把 trafficState.UserUUID 拷进
	// writeOnceUserUUID，而服务端的 trafficState 是用客户端首帧里那个
	// UUID 构造的。所以下行首帧同样以 16 字节 UUID 开头——这一点我
	// 来回改了两次才确认：不带的话客户端的 unpadding 认不出帧，会把
	// 整帧当应用数据。
	var writeUUID []byte
	if len(cloned) == 1 {
		writeUUID = append([]byte(nil), cloned[0]...)
	}
	return &visionState{
		candidates:           cloned,
		packetsToFilter:      visionPacketsToFilter,
		remainingServerHello: -1,
		withinPaddingBuffers: true,
		remainingCommand:     -1,
		remainingContent:     -1,
		remainingPadding:     -1,
		isPadding:            true,
		writeOnceUUID:        writeUUID,
	}
}

// VisionConn 把一条已完成 VLESS 握手的连接包成 Vision 语义。
//
// 读写两侧各自可以独立切进直通：上游就是这么设计的，方向之间不同步。
type VisionConn struct {
	net.Conn
	state *visionState

	readMu  sync.Mutex
	readBuf bytes.Buffer // 已剥离 padding、等待被 Read 取走的内容
	scratch []byte
	// pending 攒够首帧的前 21 字节再交给状态机。
	//
	// 上游在初始态要求 b.Len() >= 21 才比对 UUID，否则原样放行——那在
	// 它的 MultiBuffer 模型下成立，一个 buffer 就是完整的一段。换到
	// net.Conn 的流式语义就不成立了：TCP 可以把这 21 字节分几次返回，
	// 照抄的结果是首帧永远认不出来，整条连接退化成裸转发。
	pending []byte

	writeMu sync.Mutex
}

// NewVisionConn 包装一条已知用户的连接。
func NewVisionConn(conn net.Conn, userUUID []byte) (*VisionConn, error) {
	if len(userUUID) != 16 {
		return nil, errors.New("vision: userUUID 必须是 16 字节")
	}
	return NewVisionConnFor(conn, [][]byte{userUUID})
}

// NewVisionConnFor 包装一条还不知道是谁的连接。
//
// 这是服务端真正需要的形式：Vision 把 VLESS 请求头也裹进了首帧，所以
// 解出用户身份之前就得先解包，而解包又要靠 UUID 认帧。出路是把这个
// 入站上全部已授权用户的 UUID 都交进来，命中哪个算哪个。
//
// 候选为空是允许的：那种情况下不可能命中，连接会被判定为非 Vision 流
// 并透明放行，正好覆盖「节点没配 flow」的场景。
func NewVisionConnFor(conn net.Conn, candidates [][]byte) (*VisionConn, error) {
	for _, c := range candidates {
		if len(c) != 16 {
			return nil, errors.New("vision: 候选 UUID 必须是 16 字节")
		}
	}
	return &VisionConn{
		Conn:    conn,
		state:   newVisionState(candidates),
		scratch: make([]byte, visionBufferSize),
	}, nil
}

// MatchedUUID 返回首帧命中的用户 UUID；未命中或还没探测时返回 nil。
func (c *VisionConn) MatchedUUID() []byte {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	if c.state.userUUID == nil {
		return nil
	}
	return append([]byte(nil), c.state.userUUID...)
}

// IsVision 表示首帧探测是否认定这是一条 Vision 流。
func (c *VisionConn) IsVision() bool {
	c.state.mu.Lock()
	defer c.state.mu.Unlock()
	return c.state.probed && !c.state.notVision
}

func (c *VisionConn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()

	for c.readBuf.Len() == 0 {
		n, err := c.Conn.Read(c.scratch)
		visionTracef("read n=%d err=%v", n, err)
		if n > 0 {
			chunk := c.scratch[:n]
			c.state.mu.Lock()
			direct := c.state.readerDirect
			needUnpad := !direct && (c.state.withinPaddingBuffers || c.state.packetsToFilter > 0)
			atStart := c.state.remainingCommand == -1 &&
				c.state.remainingContent == -1 && c.state.remainingPadding == -1
			c.state.mu.Unlock()

			if needUnpad && atStart {
				// 还在等首帧头。攒够 21 字节才有得判，攒不够就先收着，
				// 这一轮不产出——连接不会因此卡住，对端还会继续发。
				c.pending = append(c.pending, chunk...)
				if len(c.pending) < visionFrameOverhead && err == nil {
					continue
				}
				chunk = c.pending
				c.pending = nil
			} else if len(c.pending) > 0 {
				chunk = append(c.pending, chunk...)
				c.pending = nil
			}

			c.state.mu.Lock()
			if needUnpad {
				chunk = c.state.unpad(chunk)
			}
			if c.state.packetsToFilter > 0 && len(chunk) > 0 {
				c.state.filterTLS(chunk)
			}
			c.state.mu.Unlock()
			if len(chunk) > 0 {
				c.readBuf.Write(chunk)
			}
		}
		if err != nil {
			// 连接断在首帧头中间：攒下的那点字节原样交出去，
			// 丢掉它等于让调用方看到一段凭空缺失的数据。
			if len(c.pending) > 0 {
				c.readBuf.Write(c.pending)
				c.pending = nil
			}
			if c.readBuf.Len() > 0 {
				break
			}
			return 0, err
		}
	}
	return c.readBuf.Read(p)
}

// Write 给下行数据加填充。
//
// 上游的 vless inbound 里，clientWriter 来自 EncodeBodyAddons，flow 是
// xtls-rprx-vision 时它就是一个 VisionWriter（isUplink=false）。所以
// 下行同样封包，首帧同样带 UUID。
//
// VisionConn 只在客户端声明了 flow 时才创建，走到这里就一定是 Vision
// 流，不需要再判断——一度对所有连接都套 VisionConn 时才需要那道门，
// 那个设计已经撤了。
func (c *VisionConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.state.mu.Lock()
	if c.state.writerDirect {
		c.state.mu.Unlock()
		return c.Conn.Write(p)
	}
	// TLS 识别要看双向流量：能否切直通，取决于从下行 ServerHello 里
	// 认出内层是不是 TLS 1.3。
	if c.state.packetsToFilter > 0 {
		c.state.filterTLS(p)
	}
	if !c.state.isPadding {
		c.state.mu.Unlock()
		return c.Conn.Write(p)
	}
	frames := c.state.buildPaddedFrames(p)
	c.state.mu.Unlock()

	for i, frame := range frames {
		_, err := c.Conn.Write(frame)
		visionTracef("write frame#%d len=%d err=%v", i, len(frame), err)
		if err != nil {
			return 0, err
		}
	}
	// 报告调用方交进来的字节数。填充是我们加的，算进去会让上层的流量
	// 统计每帧凭空多出几百字节。
	return len(p), nil
}

//------------------------------------------------------------------------------
// 读侧：剥离 padding
//------------------------------------------------------------------------------

// asClient 把这个状态切成客户端上行语义：首帧带 16 字节 UUID 前缀。
//
// 服务端（默认）不带——客户端已经知道自己是谁，多出来的 16 字节会被它
// 当成帧内容，此后每一帧的边界都错开 16 字节。将来做 Vision 出站时
// 走这条。
func (s *visionState) asClient(uuid []byte) *visionState {
	s.writeOnceUUID = append([]byte(nil), uuid...)
	return s
}

// matchCandidate 在已授权用户里找出帧首 16 字节对应的那个。
//
// 除了比对 UUID，还要求紧随其后的命令字节是三个合法值之一。多这一道
// 校验是因为：假如某个用户的 UUID 恰好等于流中某 16 字节，只靠 UUID
// 会误判，而误判的后果是把真实数据当 padding 丢掉。
// 调用方须持有 state.mu。
func (s *visionState) matchCandidate(b []byte) []byte {
	if len(b) < visionFrameOverhead {
		return nil
	}
	switch b[16] {
	case visionCommandPaddingContinue, visionCommandPaddingEnd, visionCommandPaddingDirect:
	default:
		return nil
	}
	for _, c := range s.candidates {
		if len(c) == 16 && bytes.Equal(c, b[:16]) {
			return c
		}
	}
	return nil
}

// unpad 是逐字节状态机，对应上游 XtlsUnpadding。
//
// 之所以逐字节而不是「凑齐 5 字节再解」，是因为一个 TCP 读返回的边界
// 可以落在帧头中间——头 5 个字节完全可能分两次到达。
//
// 调用方须持有 state.mu。
func (s *visionState) unpad(b []byte) []byte {
	if s.notVision {
		return b
	}
	if s.remainingCommand == -1 && s.remainingContent == -1 && s.remainingPadding == -1 {
		// 初始态：帧首 16 字节必须命中某个已授权用户的 UUID。
		//
		// 探测只做一次。做完就把结论钉死：命中则锁定该 UUID，没命中则
		// 认定整条流都不是 Vision。否则一条普通 VLESS 连接的每次 Read
		// 都会拿流中间的字节去当帧首比对，早晚撞上一次假命中，那时数据
		// 已经被当成 padding 吃掉了。
		matched := s.matchCandidate(b)
		if matched != nil {
			// 只锁定身份，不碰 writeOnceUUID：认出对端是谁，不等于
			// 回程也要带 UUID 前缀，那是客户端上行独有的。
			s.userUUID = matched
			s.probed = true
			b = b[16:]
			s.remainingCommand = 5
		} else {
			if s.probed || len(b) >= visionFrameOverhead {
				// 攒够了还是不匹配，就是普通流量，之后一律放行。
				s.notVision = true
			}
			return b
		}
	}

	out := make([]byte, 0, len(b))
	for len(b) > 0 {
		switch {
		case s.remainingCommand > 0:
			data := b[0]
			b = b[1:]
			switch s.remainingCommand {
			case 5:
				s.currentCommand = int(data)
			case 4:
				s.remainingContent = int32(data) << 8
			case 3:
				s.remainingContent |= int32(data)
			case 2:
				s.remainingPadding = int32(data) << 8
			case 1:
				s.remainingPadding |= int32(data)
			}
			s.remainingCommand--
		case s.remainingContent > 0:
			n := int(s.remainingContent)
			if len(b) < n {
				n = len(b)
			}
			out = append(out, b[:n]...)
			b = b[n:]
			s.remainingContent -= int32(n)
		default: // remainingPadding > 0
			n := int(s.remainingPadding)
			if len(b) < n {
				n = len(b)
			}
			b = b[n:]
			s.remainingPadding -= int32(n)
		}

		if s.remainingCommand <= 0 && s.remainingContent <= 0 && s.remainingPadding <= 0 {
			// 一个块读完了。command 为 0 表示后面还有带填充的块；
			// 非 0 表示填充到此为止，剩下的字节都是裸内容。
			if s.currentCommand == 0 {
				s.remainingCommand = 5
				continue
			}
			s.remainingCommand = -1
			s.remainingContent = -1
			s.remainingPadding = -1
			out = append(out, b...)
			break
		}
	}

	// 状态推进：读到 command=1 停止拆填充但继续走本层；
	// command=2 直接把后续数据交回裸连接。
	if s.remainingContent > 0 || s.remainingPadding > 0 || s.currentCommand == 0 {
		s.withinPaddingBuffers = true
	} else {
		switch s.currentCommand {
		case 1:
			s.withinPaddingBuffers = false
		case 2:
			s.withinPaddingBuffers = false
			s.readerDirect = true
		}
	}
	return out
}

//------------------------------------------------------------------------------
// 写侧：加 padding
//------------------------------------------------------------------------------

// buildPaddedFrames 把一段待发数据切片并逐片加填充。
// 调用方须持有 state.mu。
func (s *visionState) buildPaddedFrames(p []byte) [][]byte {
	chunks := reshapeForVision(p)
	isComplete := isCompleteTLSRecord(p)
	longPadding := s.isTLS
	frames := make([][]byte, 0, len(chunks))

	for i, chunk := range chunks {
		last := i == len(chunks)-1

		if s.isTLS && len(chunk) >= 6 && bytes.Equal(visionTLSAppDataStart, chunk[:3]) && isComplete {
			// 内层已经进入应用数据阶段。确认过是 TLS 1.3 就可以收工，
			// 让后续流量裸转——这正是 Vision 省下双重加密的地方。
			if s.enableXtls {
				s.writerDirect = true
			}
			command := visionCommandPaddingContinue
			if last {
				command = visionCommandPaddingEnd
				if s.enableXtls {
					command = visionCommandPaddingDirect
				}
			}
			frames = append(frames, s.pad(chunk, command, true))
			s.isPadding = false
			longPadding = false
			continue
		}

		if !s.isTLS12OrAbove && s.packetsToFilter <= 1 {
			// 兼容早期 Vision 接收端：非 TLS 流量提前一个包结束填充。
			s.isPadding = false
			frames = append(frames, s.pad(chunk, visionCommandPaddingEnd, longPadding))
			// 上游这里是 break，跳出后整个 MultiBuffer 仍会写出去——
			// 结束的是「填充」，不是「发送」。剩下的分片必须原样送走，
			// 否则对端收到的是被截断的流。
			frames = append(frames, chunks[i+1:]...)
			return frames
		}

		command := visionCommandPaddingContinue
		if last && !s.isPadding {
			command = visionCommandPaddingEnd
			if s.enableXtls {
				command = visionCommandPaddingDirect
			}
		}
		frames = append(frames, s.pad(chunk, command, longPadding))
	}
	return frames
}

// pad 生成一帧：[UUID(仅首帧)][command][contentLen:2][paddingLen:2][content][padding]
// 调用方须持有 state.mu。
func (s *visionState) pad(content []byte, command byte, longPadding bool) []byte {
	contentLen := int32(len(content))
	var paddingLen int32
	if contentLen < visionSeedShortContent && longPadding {
		paddingLen = int32(visionRandom(visionSeedLongRandom)) + visionSeedLongBase - contentLen
	} else {
		paddingLen = int32(visionRandom(visionSeedShortRandom))
	}
	if limit := int32(visionBufferSize-visionFrameOverhead) - contentLen; paddingLen > limit {
		paddingLen = limit
	}
	if paddingLen < 0 {
		paddingLen = 0
	}

	frame := make([]byte, 0, len(s.writeOnceUUID)+5+len(content)+int(paddingLen))
	if len(s.writeOnceUUID) > 0 {
		frame = append(frame, s.writeOnceUUID...)
		s.writeOnceUUID = nil
	}
	var head [5]byte
	head[0] = command
	binary.BigEndian.PutUint16(head[1:3], uint16(contentLen))
	binary.BigEndian.PutUint16(head[3:5], uint16(paddingLen))
	frame = append(frame, head[:]...)
	frame = append(frame, content...)
	// 填充内容不需要随机：它在外层 TLS 里，观测者看到的只有长度。
	// 上游用的也是未初始化的缓冲区扩展。
	frame = append(frame, make([]byte, paddingLen)...)
	return frame
}

// visionRandom 返回 [0, n) 的随机数。用 crypto/rand：填充长度是抗统计
// 分析的手段，可预测的长度序列本身就是特征。
func visionRandom(n int64) int64 {
	if n <= 0 {
		return 0
	}
	v, err := rand.Int(rand.Reader, big.NewInt(n))
	if err != nil {
		return 0
	}
	return v.Int64()
}

// reshapeForVision 把过长的数据切开，让每帧连头带尾不超过一个缓冲区。
//
// 切点优先落在最后一个 TLS 记录边界上：从记录中间切开会让两个分片各自
// 都不像完整记录，接收端的 TLS 识别就跟着失准。
func reshapeForVision(p []byte) [][]byte {
	const limit = visionBufferSize - visionFrameOverhead
	if len(p) < limit {
		return [][]byte{p}
	}
	var out [][]byte
	for len(p) >= limit {
		index := bytes.LastIndex(p[:limit], visionTLSAppDataStart)
		if index < visionFrameOverhead || index > limit {
			index = visionBufferSize / 2
		}
		out = append(out, p[:index])
		p = p[index:]
	}
	if len(p) > 0 {
		out = append(out, p)
	}
	return out
}

// isCompleteTLSRecord 判断这段数据是否恰好由若干个完整的 TLS 应用数据
// 记录组成。只有完整时才敢切直通——半个记录切过去，接收端会把下半个
// 当成新记录的开头。
func isCompleteTLSRecord(b []byte) bool {
	for i := 0; i < len(b); {
		if len(b)-i < 5 {
			return false
		}
		if !bytes.Equal(b[i:i+3], visionTLSAppDataStart) {
			return false
		}
		recordLen := int(binary.BigEndian.Uint16(b[i+3 : i+5]))
		i += 5 + recordLen
		if i > len(b) {
			return false
		}
	}
	return true
}

//------------------------------------------------------------------------------
// TLS 识别
//------------------------------------------------------------------------------

// filterTLS 从流量里认出 TLS 版本与密码套件，决定能否切直通。
// 对应上游 XtlsFilterTls。调用方须持有 state.mu。
func (s *visionState) filterTLS(b []byte) {
	s.packetsToFilter--

	if len(b) >= 6 {
		switch {
		case bytes.Equal(visionTLSServerHelloStart, b[:3]) && b[5] == visionHandshakeTypeServerHello:
			// 括号不能省：Go 里 + 的优先级高于 |，写成
			// `int32(b[3])<<8 | int32(b[4]) + 5` 会变成
			// `高字节 | (低字节+5)`，低字节加 5 进位时结果就错了。
			s.remainingServerHello = (int32(b[3])<<8 | int32(b[4])) + 5
			s.isTLS12OrAbove = true
			s.isTLS = true
			if len(b) >= 79 && s.remainingServerHello >= 79 {
				sessionIDLen := int32(b[43])
				if end := 43 + sessionIDLen + 3; int(end) <= len(b) {
					suite := b[43+sessionIDLen+1 : end]
					s.cipher = uint16(suite[0])<<8 | uint16(suite[1])
				}
			}
		case bytes.Equal(visionTLSClientHelloStart, b[:2]) && b[5] == visionHandshakeTypeClientHello:
			s.isTLS = true
		}
	}

	if s.remainingServerHello > 0 {
		end := s.remainingServerHello
		if end > int32(len(b)) {
			end = int32(len(b))
		}
		s.remainingServerHello -= int32(len(b))
		if bytes.Contains(b[:end], visionTLS13SupportedVersions) {
			// TLS 1.3 且不是 CCM_8：内层已经足够强，外层可以停手。
			if name, ok := visionTLS13Ciphers[s.cipher]; ok && name != "TLS_AES_128_CCM_8_SHA256" {
				s.enableXtls = true
			}
			s.packetsToFilter = 0
			return
		}
		if s.remainingServerHello <= 0 {
			// TLS 1.2 或更低，不启用直通。
			s.packetsToFilter = 0
			return
		}
	}
}

var _ io.ReadWriteCloser = (*VisionConn)(nil)
