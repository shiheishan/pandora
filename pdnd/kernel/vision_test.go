package kernel

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"testing"
)

// 这层是 wire format：第三方客户端按上游的字节布局收发，差一个字节就是
// 连不上，而且症状是「握手过了但不通」，最难查。所以帧布局、状态机分支
// 都按上游的语义钉死。

func visionTestUUID() []byte {
	u := make([]byte, 16)
	for i := range u {
		u[i] = byte(i + 1)
	}
	return u
}

// 造一帧：[UUID(可选)][command][contentLen:2][paddingLen:2][content][padding]
func makeVisionFrame(uuid []byte, command byte, content []byte, paddingLen int) []byte {
	frame := append([]byte(nil), uuid...)
	var head [5]byte
	head[0] = command
	binary.BigEndian.PutUint16(head[1:3], uint16(len(content)))
	binary.BigEndian.PutUint16(head[3:5], uint16(paddingLen))
	frame = append(frame, head[:]...)
	frame = append(frame, content...)
	frame = append(frame, make([]byte, paddingLen)...)
	return frame
}

func TestVisionUnpad(t *testing.T) {
	uuid := visionTestUUID()

	t.Run("单帧剥离填充", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		want := []byte("hello world")
		got := s.unpad(makeVisionFrame(uuid, visionCommandPaddingEnd, want, 64))
		if !bytes.Equal(got, want) {
			t.Errorf("得到 %q，期望 %q", got, want)
		}
	})

	// 首帧前缀必须是本用户的 UUID。对不上就不是 Vision 帧，
	// 上游的做法是原样放行而不是报错——那可能是别的合法流量。
	t.Run("UUID 不匹配则原样返回", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		other := make([]byte, 16) // 全零，和 uuid 不同
		raw := makeVisionFrame(other, visionCommandPaddingEnd, []byte("payload"), 8)
		got := s.unpad(raw)
		if !bytes.Equal(got, raw) {
			t.Error("UUID 不匹配时应当原样返回，不该尝试拆帧")
		}
	})

	// 首帧头之后被拆散也要能解——unpad 本身是逐字节状态机。
	// 注意切点从 21 起：更早的切点属于「首帧头还没攒齐」，那是 Read
	// 层的缓冲职责，见 TestVisionConnSplitFirstFrame。
	t.Run("帧头之后被拆散也要能解", func(t *testing.T) {
		want := []byte("split header payload")
		full := makeVisionFrame(uuid, visionCommandPaddingEnd, want, 32)
		for cut := visionFrameOverhead; cut < len(full); cut++ {
			s := newVisionState([][]byte{uuid})
			var got []byte
			got = append(got, s.unpad(full[:cut])...)
			got = append(got, s.unpad(full[cut:])...)
			if !bytes.Equal(got, want) {
				t.Fatalf("在第 %d 字节切开后解出 %q，期望 %q", cut, got, want)
			}
		}
	})

	t.Run("连续多帧", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		f1 := makeVisionFrame(uuid, visionCommandPaddingContinue, []byte("AAA"), 16)
		f2 := makeVisionFrame(nil, visionCommandPaddingContinue, []byte("BBB"), 8)
		f3 := makeVisionFrame(nil, visionCommandPaddingEnd, []byte("CCC"), 4)
		var got []byte
		got = append(got, s.unpad(f1)...)
		got = append(got, s.unpad(f2)...)
		got = append(got, s.unpad(f3)...)
		if string(got) != "AAABBBCCC" {
			t.Errorf("多帧拼接得到 %q，期望 AAABBBCCC", got)
		}
	})

	// command=2 之后本层就不该再插手，剩下的字节原样交出去。
	t.Run("command=direct 切直通并带出尾随数据", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		frame := makeVisionFrame(uuid, visionCommandPaddingDirect, []byte("head"), 4)
		trailing := []byte("raw-bytes-after-switch")
		got := s.unpad(append(frame, trailing...))
		if want := "head" + string(trailing); string(got) != want {
			t.Errorf("得到 %q，期望 %q", got, want)
		}
		if !s.readerDirect {
			t.Error("command=2 之后 readerDirect 应当为 true")
		}
	})

	t.Run("command=end 停止拆填充但不切直通", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		s.unpad(makeVisionFrame(uuid, visionCommandPaddingEnd, []byte("x"), 4))
		if s.readerDirect {
			t.Error("command=1 不该切直通")
		}
		if s.withinPaddingBuffers {
			t.Error("command=1 之后应当退出填充模式")
		}
	})

	t.Run("零长内容的纯填充帧", func(t *testing.T) {
		s := newVisionState([][]byte{uuid})
		got := s.unpad(makeVisionFrame(uuid, visionCommandPaddingContinue, nil, 128))
		if len(got) != 0 {
			t.Errorf("纯填充帧不该产出内容，得到 %q", got)
		}
	})
}

// 服务端在解出 VLESS 请求头之前不知道来的是谁，只能拿全量已授权用户的
// UUID 去比对帧首。这组用例守的就是那个比对。
func TestVisionMultiCandidate(t *testing.T) {
	mk := func(seed byte) []byte {
		u := make([]byte, 16)
		for i := range u {
			u[i] = seed + byte(i)
		}
		return u
	}
	alice, bob, carol := mk(1), mk(100), mk(200)

	t.Run("命中候选集里的任意一个", func(t *testing.T) {
		for _, who := range [][]byte{alice, bob, carol} {
			s := newVisionState([][]byte{alice, bob, carol})
			want := []byte("multi-candidate payload")
			got := s.unpad(makeVisionFrame(who, visionCommandPaddingEnd, want, 16))
			if !bytes.Equal(got, want) {
				t.Fatalf("命中失败：解出 %q，期望 %q", got, want)
			}
			if !bytes.Equal(s.userUUID, who) {
				t.Errorf("锁定的 UUID 不对：%x，期望 %x", s.userUUID, who)
			}
		}
	})

	t.Run("不在候选集里的 UUID 不命中", func(t *testing.T) {
		s := newVisionState([][]byte{alice, bob})
		raw := makeVisionFrame(carol, visionCommandPaddingEnd, []byte("x"), 8)
		if got := s.unpad(raw); !bytes.Equal(got, raw) {
			t.Error("陌生 UUID 应当原样放行")
		}
		if !s.notVision {
			t.Error("探测失败后应当标记为非 Vision 流")
		}
	})

	// 一条普通 VLESS 连接的字节流里，任意 16 字节都可能巧合等于某个
	// UUID。只靠 UUID 判断，早晚会把真实数据当 padding 吃掉。所以还要
	// 求紧随其后的命令字节合法。
	t.Run("命令字节非法时不认帧", func(t *testing.T) {
		s := newVisionState([][]byte{alice})
		raw := append(append([]byte(nil), alice...), 0x7f) // 0x7f 不是合法命令
		raw = append(raw, make([]byte, 32)...)
		if got := s.unpad(raw); !bytes.Equal(got, raw) {
			t.Error("命令字节非法时应当原样放行")
		}
	})

	// 探测只做一次。否则普通流量的每次 Read 都要重新赌一遍。
	t.Run("判定非 Vision 后永久直通", func(t *testing.T) {
		s := newVisionState([][]byte{alice})
		first := bytes.Repeat([]byte{0x5a}, 64)
		s.unpad(first)
		if !s.notVision {
			t.Fatal("首次探测失败应当立刻定性")
		}
		// 后续哪怕真的出现一个长得像帧首的片段，也不能再被当成帧
		lookalike := makeVisionFrame(alice, visionCommandPaddingEnd, []byte("data"), 8)
		if got := s.unpad(lookalike); !bytes.Equal(got, lookalike) {
			t.Error("定性之后不该再尝试拆帧")
		}
	})

	// 候选为空对应「节点没配 flow」：不可能命中，必须透明放行。
	t.Run("空候选集透明放行", func(t *testing.T) {
		s := newVisionState(nil)
		raw := makeVisionFrame(alice, visionCommandPaddingEnd, []byte("y"), 8)
		if got := s.unpad(raw); !bytes.Equal(got, raw) {
			t.Error("没有候选时应当原样放行")
		}
	})

	// UUID 前缀是客户端上行首帧独有的。服务端回程带上它，客户端会把这
	// 16 字节当成帧内容，此后每一帧的边界都错开——这是拿真实 Mihomo
	// 抓出来的一个真 bug。
	t.Run("服务端回程首帧不带 UUID", func(t *testing.T) {
		s := newVisionState([][]byte{alice, bob})
		s.unpad(makeVisionFrame(bob, visionCommandPaddingEnd, []byte("z"), 8))
		frame := s.pad([]byte("reply"), visionCommandPaddingEnd, false)
		if len(frame) >= 16 && (bytes.Equal(frame[:16], bob) || bytes.Equal(frame[:16], alice)) {
			t.Errorf("回程首帧不该带 UUID，实际以 %x 开头", frame[:16])
		}
		if frame[0] != visionCommandPaddingEnd {
			t.Errorf("回程首帧应当直接以 command 开头，实际 %#x", frame[0])
		}
	})

	// 反过来，客户端上行必须带——将来做 Vision 出站时靠它认人。
	t.Run("客户端上行首帧带 UUID", func(t *testing.T) {
		s := newVisionState([][]byte{alice}).asClient(alice)
		frame := s.pad([]byte("hello"), visionCommandPaddingContinue, false)
		if !bytes.Equal(frame[:16], alice) {
			t.Errorf("客户端上行首帧应当以 UUID 开头，实际 %x", frame[:16])
		}
	})
}

func TestVisionConnProbeAPI(t *testing.T) {
	uuid := visionTestUUID()
	t.Run("非 16 字节候选被拒", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		if _, err := NewVisionConnFor(c1, [][]byte{make([]byte, 15)}); err == nil {
			t.Error("长度不对的候选应当被拒")
		}
	})

	t.Run("命中后可查到是谁", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		reader, err := NewVisionConnFor(server, [][]byte{uuid})
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte("who am i")
		go func() {
			_, _ = client.Write(makeVisionFrame(uuid, visionCommandPaddingEnd, payload, 24))
			_ = client.Close()
		}()
		got, _ := io.ReadAll(reader)
		if !bytes.Equal(got, payload) {
			t.Fatalf("解出 %q，期望 %q", got, payload)
		}
		if !reader.IsVision() {
			t.Error("命中后 IsVision 应当为 true")
		}
		if !bytes.Equal(reader.MatchedUUID(), uuid) {
			t.Errorf("MatchedUUID = %x，期望 %x", reader.MatchedUUID(), uuid)
		}
	})

	// 普通 VLESS 连接必须原样穿过，一个字节都不能少。
	t.Run("非 Vision 流原样穿过", func(t *testing.T) {
		client, server := net.Pipe()
		defer client.Close()
		defer server.Close()
		reader, err := NewVisionConnFor(server, [][]byte{uuid})
		if err != nil {
			t.Fatal(err)
		}
		// 裸 VLESS 请求头的形状：version(0) + uuid + ...
		plain := append([]byte{0x00}, uuid...)
		plain = append(plain, []byte("\x00\x01\x00\x50\x01abcd")...)
		go func() {
			_, _ = client.Write(plain)
			_ = client.Close()
		}()
		got, _ := io.ReadAll(reader)
		if !bytes.Equal(got, plain) {
			t.Errorf("裸流被改动了：得到 %x，期望 %x", got, plain)
		}
		if reader.IsVision() {
			t.Error("裸流不该被判成 Vision")
		}
	})
}

func TestVisionPadRoundTrip(t *testing.T) {
	uuid := visionTestUUID()
	// 写侧产出的帧，读侧必须能原样解回来——两边不一致就是「自己都
	// 对不上」，更别说第三方客户端。
	for _, size := range []int{0, 1, 100, 900, 1500, 8000} {
		payload := bytes.Repeat([]byte{0xAB}, size)
		w := newVisionState([][]byte{uuid}).asClient(uuid)
		r := newVisionState([][]byte{uuid})
		var wire []byte
		for _, f := range w.buildPaddedFrames(payload) {
			wire = append(wire, f...)
		}
		got := r.unpad(wire)
		if !bytes.Equal(got, payload) {
			t.Errorf("size=%d 往返不一致：解出 %d 字节，期望 %d", size, len(got), size)
		}
	}
}

func TestVisionPadLayout(t *testing.T) {
	uuid := visionTestUUID()
	// 验的是客户端上行帧的完整布局——只有那个方向带 UUID 前缀。
	s := newVisionState([][]byte{uuid}).asClient(uuid)
	content := []byte("payload")
	frame := s.pad(content, visionCommandPaddingEnd, false)

	if !bytes.Equal(frame[:16], uuid) {
		t.Error("首帧必须以 16 字节 UUID 开头")
	}
	if frame[16] != visionCommandPaddingEnd {
		t.Errorf("command 字节 = %#x，期望 %#x", frame[16], visionCommandPaddingEnd)
	}
	if got := binary.BigEndian.Uint16(frame[17:19]); int(got) != len(content) {
		t.Errorf("contentLen = %d，期望 %d", got, len(content))
	}
	padLen := binary.BigEndian.Uint16(frame[19:21])
	if want := 21 + len(content) + int(padLen); len(frame) != want {
		t.Errorf("帧总长 %d，按头部声明应为 %d", len(frame), want)
	}
	if !bytes.Equal(frame[21:21+len(content)], content) {
		t.Error("内容位置不对")
	}

	// UUID 只在第一帧出现一次，后续帧再带就会被对端当成内容。
	second := s.pad(content, visionCommandPaddingEnd, false)
	// 非首帧长度是 5 + 内容 + 随机填充，填充随机取到 0 且内容短时整帧
	// 不足 16 字节，直接切片会 panic。短于 16 字节本身就说明没带 UUID，
	// 断言照样成立。
	if len(second) >= 16 && bytes.Equal(second[:16], uuid) {
		t.Error("UUID 只应出现在首帧")
	}
	if second[0] != visionCommandPaddingEnd {
		t.Error("非首帧应当直接以 command 开头")
	}
}

func TestVisionFilterTLS(t *testing.T) {
	// 只有确认内层是 TLS 1.3、且密码套件不是 CCM_8，才允许切直通。
	// 判错的代价是把未加密或弱加密的流量裸转出去。
	build := func(cipher uint16, withTLS13Ext bool) []byte {
		b := make([]byte, 200)
		copy(b, visionTLSServerHelloStart) // 16 03 03
		b[3], b[4] = 0x00, 0xC3            // record length
		b[5] = visionHandshakeTypeServerHello
		b[43] = 0 // session id 长度
		b[44] = byte(cipher >> 8)
		b[45] = byte(cipher)
		if withTLS13Ext {
			copy(b[100:], visionTLS13SupportedVersions)
		}
		return b
	}

	t.Run("TLS 1.3 且套件合格则启用直通", func(t *testing.T) {
		s := newVisionState([][]byte{visionTestUUID()})
		s.filterTLS(build(0x1301, true)) // TLS_AES_128_GCM_SHA256
		if !s.enableXtls {
			t.Error("TLS 1.3 + AES_128_GCM 应当启用直通")
		}
	})

	t.Run("CCM_8 不启用直通", func(t *testing.T) {
		s := newVisionState([][]byte{visionTestUUID()})
		s.filterTLS(build(0x1305, true)) // TLS_AES_128_CCM_8_SHA256
		if s.enableXtls {
			t.Error("CCM_8 的标签只有 8 字节，上游不对它启用直通")
		}
	})

	t.Run("没有 TLS 1.3 扩展则不启用", func(t *testing.T) {
		s := newVisionState([][]byte{visionTestUUID()})
		s.filterTLS(build(0x1301, false))
		if s.enableXtls {
			t.Error("识别不到 TLS 1.3 时不该启用直通")
		}
		if !s.isTLS12OrAbove {
			t.Error("ServerHello 已经说明这是 TLS 1.2+")
		}
	})

	t.Run("ClientHello 只标记 isTLS", func(t *testing.T) {
		s := newVisionState([][]byte{visionTestUUID()})
		b := make([]byte, 100)
		copy(b, visionTLSClientHelloStart)
		b[5] = visionHandshakeTypeClientHello
		s.filterTLS(b)
		if !s.isTLS {
			t.Error("ClientHello 应当标记 isTLS")
		}
		if s.enableXtls {
			t.Error("光看到 ClientHello 不足以启用直通")
		}
	})

	t.Run("识别窗口有限", func(t *testing.T) {
		s := newVisionState([][]byte{visionTestUUID()})
		for i := 0; i < visionPacketsToFilter+2; i++ {
			s.filterTLS([]byte("not tls at all......"))
		}
		if s.packetsToFilter > 0 {
			t.Errorf("超过 %d 个包后应当停止识别，剩余 %d",
				visionPacketsToFilter, s.packetsToFilter)
		}
	})
}

func TestIsCompleteTLSRecord(t *testing.T) {
	rec := func(payloadLen int) []byte {
		b := append([]byte(nil), visionTLSAppDataStart...)
		var l [2]byte
		binary.BigEndian.PutUint16(l[:], uint16(payloadLen))
		b = append(b, l[:]...)
		return append(b, make([]byte, payloadLen)...)
	}

	if !isCompleteTLSRecord(rec(100)) {
		t.Error("单个完整记录应当判为完整")
	}
	if !isCompleteTLSRecord(append(rec(10), rec(20)...)) {
		t.Error("连续两个完整记录应当判为完整")
	}
	// 半个记录切过去，接收端会把下半个当成新记录的开头。
	if isCompleteTLSRecord(rec(100)[:50]) {
		t.Error("截断的记录不该判为完整")
	}
	if isCompleteTLSRecord([]byte{0x17, 0x03}) {
		t.Error("连记录头都不全")
	}
	if isCompleteTLSRecord([]byte("plain http request")) {
		t.Error("非 TLS 数据不该判为完整记录")
	}
}

func TestReshapeForVision(t *testing.T) {
	const limit = visionBufferSize - visionFrameOverhead

	t.Run("短数据不切", func(t *testing.T) {
		p := make([]byte, 100)
		if got := reshapeForVision(p); len(got) != 1 {
			t.Errorf("短数据切成了 %d 片", len(got))
		}
	})

	t.Run("超长数据每片都放得下帧头", func(t *testing.T) {
		p := bytes.Repeat([]byte{0x5A}, visionBufferSize*3)
		chunks := reshapeForVision(p)
		var total int
		for i, c := range chunks {
			if len(c) > limit {
				t.Errorf("第 %d 片 %d 字节，超过上限 %d", i, len(c), limit)
			}
			total += len(c)
		}
		if total != len(p) {
			t.Errorf("切片后总长 %d，原始 %d，丢字节了", total, len(p))
		}
	})
}

// 端到端：一侧用 VisionConn 写、另一侧用 VisionConn 读，
// 数据必须原样穿过去。
func TestVisionConnRoundTrip(t *testing.T) {
	uuid := visionTestUUID()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	writer, err := NewVisionConn(client, uuid)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := NewVisionConn(server, uuid)
	if err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("pandora-vision-"), 200)
	go func() {
		_, _ = writer.Write(payload)
		_ = client.Close()
	}()

	got, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		t.Fatalf("读取失败：%v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("穿过 Vision 后数据变了：得到 %d 字节，期望 %d", len(got), len(payload))
	}
}

// 首帧的前 21 字节被 TCP 拆开，是 net.Conn 流式语义下的常态。
// 上游在 MultiBuffer 模型里靠「一个 buffer 就是完整一段」绕过了这点，
// 照抄过来会让首帧永远认不出来、整条连接退化成裸转发——连得上、
// 数据全乱。所以 Read 层必须先把头攒齐。
func TestVisionConnSplitFirstFrame(t *testing.T) {
	uuid := visionTestUUID()
	payload := []byte("first frame split across tcp reads")
	frame := makeVisionFrame(uuid, visionCommandPaddingEnd, payload, 48)

	// 逐字节喂：最坏情况下每次只到 1 个字节
	for _, chunkSize := range []int{1, 3, 7, 20, 21, 33} {
		t.Run("每次"+string(rune('0'+chunkSize/10))+string(rune('0'+chunkSize%10))+"字节", func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			reader, err := NewVisionConn(server, uuid)
			if err != nil {
				t.Fatal(err)
			}
			go func() {
				for i := 0; i < len(frame); i += chunkSize {
					end := i + chunkSize
					if end > len(frame) {
						end = len(frame)
					}
					_, _ = client.Write(frame[i:end])
				}
				_ = client.Close()
			}()

			got, err := io.ReadAll(reader)
			if err != nil && err != io.EOF {
				t.Fatalf("读取失败：%v", err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("每次 %d 字节时解出 %q，期望 %q", chunkSize, got, payload)
			}
		})
	}
}

// 连接断在首帧头中间时，攒下的那点字节不能凭空丢掉。
func TestVisionConnTruncatedFirstFrame(t *testing.T) {
	uuid := visionTestUUID()
	client, server := net.Pipe()
	defer server.Close()

	reader, err := NewVisionConn(server, uuid)
	if err != nil {
		t.Fatal(err)
	}
	partial := uuid[:10] // 连 UUID 都没发全就断了
	go func() {
		_, _ = client.Write(partial)
		_ = client.Close()
	}()

	got, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		t.Fatalf("读取失败：%v", err)
	}
	if !bytes.Equal(got, partial) {
		t.Errorf("截断时应当原样交出已收到的 %d 字节，实际得到 %d 字节",
			len(partial), len(got))
	}
}

func TestNewVisionConnRejectsBadUUID(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	for _, bad := range [][]byte{nil, make([]byte, 15), make([]byte, 17)} {
		if _, err := NewVisionConn(c1, bad); err == nil {
			t.Errorf("%d 字节的 UUID 应当被拒", len(bad))
		}
	}
}
