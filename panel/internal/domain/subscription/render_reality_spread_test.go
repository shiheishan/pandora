package subscription

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// REALITY 配了多个 server name / short id 时，每个用户按稳定哈希分到其中一个：
// 同一用户每次拉到的不变、三种格式一致；不同用户分散到每一个值上。

func multiRealityNode(t *testing.T) Node {
	t.Helper()
	for _, f := range formFixtures() {
		if f.id == "vless-reality-multi" {
			return kernelShapedNodes([]Node{formNode(t, f)})[0]
		}
	}
	t.Fatal("vless-reality-multi fixture missing")
	return Node{}
}

func userUUID(i int) string {
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
}

func TestRealityPicksSpreadAcrossUsers(t *testing.T) {
	n := multiRealityNode(t)
	snis, sids := map[string]int{}, map[string]int{}
	for i := 0; i < 600; i++ {
		o := parseStream(n, userUUID(i))
		snis[o.SNI]++
		sids[o.ShortID]++
	}
	if len(snis) != 2 || snis["www.example.com"] == 0 || snis["static.example.com"] == 0 {
		t.Fatalf("server names not spread: %v", snis)
	}
	if len(sids) != 3 || sids["0a1b2c3d"] == 0 || sids["4e5f"] == 0 || sids["6a7b8c9d0e1f2a3b"] == 0 {
		t.Fatalf("short ids not spread: %v", sids)
	}
	// 粗看均匀：每个值至少拿到平均份额的一半
	for value, count := range snis {
		if count < 600/2/2 {
			t.Errorf("server name %s only got %d of 600 users", value, count)
		}
	}
	for value, count := range sids {
		if count < 600/3/2 {
			t.Errorf("short id %s only got %d of 600 users", value, count)
		}
	}
}

func TestRealityPickIsStablePerUserAndConsistentAcrossFormats(t *testing.T) {
	n := multiRealityNode(t)
	for i := 0; i < 20; i++ {
		uuid := userUUID(i)
		first := parseStream(n, uuid)
		if again := parseStream(n, uuid); again.SNI != first.SNI || again.ShortID != first.ShortID {
			t.Fatalf("user %s: pick changed between calls", uuid)
		}
		// 改名不换握手参数：哈希只看用户与节点地址
		renamed := n
		renamed.Name = "renamed"
		if r := parseStream(renamed, uuid); r.SNI != first.SNI || r.ShortID != first.ShortID {
			t.Fatalf("user %s: renaming the node changed the pick", uuid)
		}

		clash, _ := nodeToClash(n, uuid, false)
		ro := clash["reality-opts"].(map[string]any)
		if clash["servername"] != first.SNI || ro["short-id"] != first.ShortID {
			t.Errorf("user %s: clash got %v/%v, want %s/%s", uuid, clash["servername"], ro["short-id"], first.SNI, first.ShortID)
		}
		sb, _ := nodeToSingbox(n, uuid)
		raw, _ := json.Marshal(sb)
		if !strings.Contains(string(raw), `"server_name":"`+first.SNI+`"`) || !strings.Contains(string(raw), `"short_id":"`+first.ShortID+`"`) {
			t.Errorf("user %s: sing-box %s does not carry %s/%s", uuid, raw, first.SNI, first.ShortID)
		}
		uri, _ := nodeToURI(n, uuid)
		if !strings.Contains(uri, "sni="+first.SNI) || !strings.Contains(uri, "sid="+first.ShortID) {
			t.Errorf("user %s: uri %s does not carry %s/%s", uuid, uri, first.SNI, first.ShortID)
		}
	}
}

func TestRealitySingleValueIsUnchanged(t *testing.T) {
	cfg := map[string]any{"server_names": []any{"www.example.com"}, "short_ids": []any{"", "0a1b"}}
	n := Node{Host: fixtureHost, Port: 443, Config: cfg}
	for i := 0; i < 10; i++ {
		// 空 short id（存量数据里可能有）不参与挑选
		if got := pickForUser(cfgStrings(cfg, "short_ids"), userUUID(i), n, "sid"); got != "0a1b" {
			t.Fatalf("pick = %q, want the only non-empty short id", got)
		}
		if got := pickForUser(cfgStrings(cfg, "server_names"), userUUID(i), n, "sni"); got != "www.example.com" {
			t.Fatalf("pick = %q, want the only server name", got)
		}
	}
	if got := pickForUser(nil, userUUID(1), n, "sni"); got != "" {
		t.Fatalf("empty list picked %q", got)
	}
}
