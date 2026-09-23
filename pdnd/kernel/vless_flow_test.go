package kernel

import (
	"bytes"
	"strings"
	"testing"
)

// addons 是客户端发来的第一段可变数据，解析出错的后果不是报错而是
// 「连得上但不通」，所以每种取值都钉死。
func TestParseVLESSAddons(t *testing.T) {
	t.Run("空 addons 合法", func(t *testing.T) {
		got, err := ParseVLESSAddons(nil)
		if err != nil {
			t.Fatalf("空输入不该报错：%v", err)
		}
		if got.Flow != "" || got.Seed != nil {
			t.Errorf("空输入应当解出零值，得到 %+v", got)
		}
	})

	t.Run("只有 flow", func(t *testing.T) {
		raw := EncodeVLESSAddons(VLESSAddons{Flow: FlowVision})
		got, err := ParseVLESSAddons(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got.Flow != FlowVision {
			t.Errorf("flow = %q，期望 %q", got.Flow, FlowVision)
		}
	})

	t.Run("flow 加 seed", func(t *testing.T) {
		want := VLESSAddons{Flow: FlowVision, Seed: []byte{1, 2, 3, 0xff}}
		got, err := ParseVLESSAddons(EncodeVLESSAddons(want))
		if err != nil {
			t.Fatal(err)
		}
		if got.Flow != want.Flow || !bytes.Equal(got.Seed, want.Seed) {
			t.Errorf("往返不一致：得到 %+v，期望 %+v", got, want)
		}
	})

	// 上游给 Addons 加字段时不该把我们直接打死——protobuf 的前向兼容
	// 就靠跳过未知字段。
	t.Run("未知字段跳过", func(t *testing.T) {
		raw := EncodeVLESSAddons(VLESSAddons{Flow: FlowVision})
		raw = append(raw, byte(7<<3|2), 3, 'a', 'b', 'c') // 字段 7，我们不认识
		got, err := ParseVLESSAddons(raw)
		if err != nil {
			t.Fatalf("未知字段不该报错：%v", err)
		}
		if got.Flow != FlowVision {
			t.Errorf("跳过未知字段后 flow 丢了：%q", got.Flow)
		}
	})

	t.Run("截断的数据要报错", func(t *testing.T) {
		raw := EncodeVLESSAddons(VLESSAddons{Flow: FlowVision})
		for cut := 1; cut < len(raw); cut++ {
			if _, err := ParseVLESSAddons(raw[:cut]); err == nil {
				t.Errorf("截到 %d 字节仍然解析成功，应当报错", cut)
			}
		}
	})

	t.Run("不支持的线型要报错", func(t *testing.T) {
		// 字段 1 用 varint 线型（0）而不是 length-delimited
		if _, err := ParseVLESSAddons([]byte{byte(1<<3 | 0), 5}); err == nil {
			t.Error("线型不对应当报错，而不是当成合法输入")
		}
	})

	t.Run("超长 flow 要报错", func(t *testing.T) {
		long := strings.Repeat("x", maxVLESSFlowLength+1)
		if _, err := ParseVLESSAddons(EncodeVLESSAddons(VLESSAddons{Flow: long})); err == nil {
			t.Error("超长 flow 应当被拒")
		}
	})

	t.Run("非 UTF-8 的 flow 要报错", func(t *testing.T) {
		raw := []byte{byte(addonsFieldFlow<<3 | 2), 2, 0xff, 0xfe}
		if _, err := ParseVLESSAddons(raw); err == nil {
			t.Error("非法 UTF-8 应当被拒")
		}
	})
}

func TestNegotiateVLESSFlow(t *testing.T) {
	t.Run("空 flow 走普通路径", func(t *testing.T) {
		vision, err := NegotiateVLESSFlow(FlowNone)
		if err != nil || vision {
			t.Errorf("得到 vision=%v err=%v，期望 false/nil", vision, err)
		}
	})

	for _, f := range []string{FlowVision, FlowVisionUDP443} {
		t.Run("识别 "+f, func(t *testing.T) {
			vision, err := NegotiateVLESSFlow(f)
			if err != nil || !vision {
				t.Errorf("得到 vision=%v err=%v，期望 true/nil", vision, err)
			}
		})
	}

	// 静默降级的表现是「能连上但打不开网页」，用户会怀疑节点、怀疑线路，
	// 唯独想不到是流控没生效。所以必须报错，且错误里要点名是哪个值。
	for _, f := range []string{flowLegacyDirect, flowLegacySplice, "xtls-rprx-vision-typo", "vision"} {
		t.Run("拒绝 "+f, func(t *testing.T) {
			vision, err := NegotiateVLESSFlow(f)
			if err == nil {
				t.Fatalf("%q 应当被拒绝，实际返回 vision=%v", f, vision)
			}
			if !strings.Contains(err.Error(), f) {
				t.Errorf("错误信息里要点名具体的值，实际：%v", err)
			}
		})
	}

	// 废弃的两个值要给出替代方案，不能只说「不支持」。
	for _, f := range []string{flowLegacyDirect, flowLegacySplice} {
		if _, err := NegotiateVLESSFlow(f); err == nil ||
			!strings.Contains(err.Error(), FlowVision) {
			t.Errorf("%q 的错误里应当指向 %s，实际：%v", f, FlowVision, err)
		}
	}
}
