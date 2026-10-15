package render

import (
	"encoding/json"
	"strings"
	"testing"
)

// Stash / SIP008 渲染矩阵：form 与 legacy 组每个夹具一行，与 renderMatrix 的 clash（Meta）格对齐推导。

func stashSIP008Matrix() map[string]struct{ stash, sip008 cell } {
	clashRows := renderMatrix()
	out := make(map[string]struct{ stash, sip008 cell }, len(clashRows))
	for name, row := range clashRows {
		out[name] = stashSIP008Row(name, row.clash)
	}
	return out
}

func stashSIP008Row(name string, clash cell) struct{ stash, sip008 cell } {
	return struct{ stash, sip008 cell }{
		stash:  stashCell(name, clash),
		sip008: sip008Cell(name),
	}
}

func sip008Cell(name string) cell {
	if name == "shadowtls" {
		return skip("ShadowTLS")
	}
	switch name {
	case "ss2022-aes128":
		return has(`"password":"`+ss2022PW128+`"`, `"method":"2022-blake3-aes-128-gcm"`)
	case "ss2022-aes256":
		return has(`"password":"`+ss2022PW256+`"`, `"method":"2022-blake3-aes-256-gcm"`)
	case "ss2022-chacha":
		return skip("多用户不支持")
	}
	if strings.HasPrefix(name, "ss-") {
		method := "aes-128-gcm"
		if name == "ss-chacha" {
			method = "chacha20-ietf-poly1305"
		}
		return has(`"method":"` + method + `"`)
	}
	return skip("只有 Shadowsocks")
}

func stashCell(name string, clash cell) cell {
	if strings.HasPrefix(name, "naive") {
		return skip("naive")
	}
	if name == "juicity" {
		return has(`type: "juicity"`)
	}
	if strings.Contains(name, "httpupgrade") || name == "trojan-tls-httpupgrade" {
		return skip("HTTP Upgrade")
	}
	if clash.skip != "" {
		reason := strings.ReplaceAll(clash.skip, "mihomo", "Stash")
		return skip(reason)
	}
	c := has()
	for _, part := range clash.has {
		if strings.Contains(part, "v2ray-http-upgrade") {
			continue
		}
		c = c.with(part)
	}
	if strings.HasPrefix(name, "hy2") {
		c = c.with(`auth: "` + fixtureUUID + `"`).without(`password: "` + fixtureUUID + `"`)
	}
	if name == "tuic" {
		c = c.with(`version: 5`)
	}
	if strings.HasPrefix(name, "vless") || strings.HasPrefix(name, "vmess") {
		for _, part := range clash.has {
			if strings.HasPrefix(part, `servername: "`) {
				sni := strings.Replace(part, "servername", "sni", 1)
				c = c.with(sni)
			}
		}
	}
	return c
}

func TestStashSIP008MatrixCoversEveryFormFixture(t *testing.T) {
	matrix := stashSIP008Matrix()
	fixtures := loadFixtures(t)
	nodes := make([]Node, 0, len(fixtures))
	for _, f := range fixtures {
		nodes = append(nodes, f.toNode())
	}
	nodes = uniqueNodeNames(nodes)
	rows := 0
	for i, n := range nodes {
		if g := fixtures[i].Group; g != groupForm && g != groupLegacy {
			continue
		}
		rows++
		row, ok := matrix[n.Name]
		if !ok {
			t.Errorf("%s: no stash/sip008 matrix row", n.Name)
			continue
		}
		stash, stashSkip := nodeToStash(n, fixtureUUID)
		checkCell(t, n.Name, "stash", row.stash, clashText(stash), stashSkip)
		sip, sipSkip := nodeToSIP008(n, fixtureUUID)
		checkCell(t, n.Name, "sip008", row.sip008, sip008Text(sip), sipSkip)
	}
	if rows != len(matrix) {
		t.Fatalf("matrix has %d rows, form+legacy fixtures %d", len(matrix), rows)
	}
}

func sip008Text(s *sip008Server) string {
	if s == nil {
		return ""
	}
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// with 在格上追加必须包含的子串（cell 的字段就叫 has，方法不能同名）。
func (c cell) with(p ...string) cell { c.has = append(c.has, p...); return c }
