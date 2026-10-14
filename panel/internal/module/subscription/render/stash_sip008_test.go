package render

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestStashRenderFormGroup(t *testing.T) {
	body, ct, count := RenderRequest(Request{
		Format: FormatStash, Nodes: loadGroup(t, groupForm), ProxyUUID: fixtureUUID,
	})
	if ct != "text/yaml; charset=utf-8" {
		t.Fatalf("content-type: %s", ct)
	}
	if count == 0 {
		t.Fatal("expected some real nodes")
	}
	text := string(body)
	if !strings.Contains(text, `auth: "`+fixtureUUID+`"`) {
		t.Error("hysteria2 should use auth instead of password")
	}
	if strings.Contains(text, "v2ray-http-upgrade") {
		t.Error("stash must not contain v2ray-http-upgrade")
	}
}

func TestSIP008Render(t *testing.T) {
	ssOnly := []Node{nodeByName(t, "ss-aes128")}
	body, ct, count := RenderRequest(Request{Format: FormatSIP008, Nodes: ssOnly, ProxyUUID: fixtureUUID})
	if ct != "application/json; charset=utf-8" || count != 1 {
		t.Fatalf("ct=%s count=%d", ct, count)
	}
	if !strings.Contains(string(body), `"version": 1`) {
		t.Error("missing version 1")
	}
	if !strings.Contains(string(body), `"method":"aes-128-gcm"`) && !strings.Contains(string(body), `"method": "aes-128-gcm"`) {
		t.Error("missing method")
	}

	withStatus, _, _ := RenderRequest(Request{
		Format: FormatSIP008, Nodes: ssOnly, ProxyUUID: fixtureUUID,
		Status: Status{Total: 100, Download: 30},
	})
	if !strings.Contains(string(withStatus), `"bytes_remaining": 70`) {
		t.Errorf("bytes_remaining: %s", withStatus)
	}

	noTotal, _, _ := RenderRequest(Request{
		Format: FormatSIP008, Nodes: ssOnly, ProxyUUID: fixtureUUID,
		Status: Status{Total: 0, Download: 30},
	})
	if strings.Contains(string(noTotal), "bytes_") {
		t.Error("Total=0 should omit bytes_*")
	}

	id1b, _, _ := RenderRequest(Request{Format: FormatSIP008, Nodes: ssOnly, ProxyUUID: fixtureUUID})
	id1 := string(id1b)
	id2b, _, _ := RenderRequest(Request{Format: FormatSIP008, Nodes: ssOnly, ProxyUUID: fixtureUUID})
	id2 := string(id2b)
	if id1 != id2 {
		t.Error("same node should yield same id")
	}

	empty, _, _ := RenderRequest(Request{Format: FormatSIP008, Nodes: nil, ProxyUUID: fixtureUUID})
	if !strings.Contains(string(empty), `"servers": []`) {
		t.Errorf("empty servers: %s", empty)
	}
}

func TestShadowrocketSTATUS(t *testing.T) {
	exp := time.Date(2026, 12, 31, 12, 0, 0, 0, time.UTC)
	body, _, _ := RenderRequest(Request{
		Format: FormatShadowrocket, Nodes: loadGroup(t, groupForm), ProxyUUID: fixtureUUID,
		Status: Status{Expire: exp},
	})
	decoded, err := base64.StdEncoding.DecodeString(string(body))
	if err != nil {
		t.Fatal(err)
	}
	first := strings.SplitN(string(decoded), "\n", 2)[0]
	if !strings.HasPrefix(first, "STATUS=🚀↑:0.00GB,↓:") {
		t.Fatalf("status prefix: %s", first)
	}
	if !strings.Contains(first, "Expires:2026-12-31") {
		t.Fatalf("expire: %s", first)
	}

	inf, _, _ := RenderRequest(Request{
		Format: FormatShadowrocket, Nodes: loadGroup(t, groupForm), ProxyUUID: fixtureUUID,
		Status: Status{Total: 0},
	})
	decInf, _ := base64.StdEncoding.DecodeString(string(inf))
	if !strings.Contains(string(decInf), "TOT:∞") {
		t.Error("Total=0 should show TOT:∞")
	}

	uriBody, _, _ := RenderRequest(Request{
		Format: FormatURI, Nodes: loadGroup(t, groupForm), ProxyUUID: fixtureUUID,
		Status: Status{Expire: exp},
	})
	uriDec, _ := base64.StdEncoding.DecodeString(string(uriBody))
	if strings.HasPrefix(string(uriDec), "STATUS=") {
		t.Error("FormatURI must not have STATUS line")
	}
}

func TestInfoLinesURIAndSIP008(t *testing.T) {
	lines := []string{"信息一", "信息二"}
	ss := nodeByName(t, "ss-aes128")

	uriBody, _, _ := RenderRequest(Request{
		Format: FormatURI, Nodes: []Node{ss}, ProxyUUID: fixtureUUID, InfoLines: lines,
	})
	dec, _ := base64.StdEncoding.DecodeString(string(uriBody))
	parts := strings.Split(strings.TrimSpace(string(dec)), "\n")
	if len(parts) < 2 {
		t.Fatalf("expected info lines first: %q", dec)
	}
	for i, want := range lines {
		if !strings.HasPrefix(parts[i], "ss://") {
			t.Errorf("line %d not ss://", i)
		}
		// url.Parse 已把 # 后的百分号编码解开：Fragment 是原文，RawFragment / EscapedFragment 才是编码后的
		u, err := url.Parse(parts[i])
		if err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if u.Fragment != want {
			t.Errorf("line %d fragment want %q got %q", i, want, u.Fragment)
		}
	}

	sipBody, _, _ := RenderRequest(Request{
		Format: FormatSIP008, Nodes: []Node{ss}, ProxyUUID: fixtureUUID, InfoLines: lines,
	})
	var doc sip008Doc
	if err := json.Unmarshal(sipBody, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Servers) < 2 || doc.Servers[0].Remarks != lines[0] || doc.Servers[1].Remarks != lines[1] {
		t.Fatalf("sip008 info remarks: %+v", doc.Servers)
	}

	mieru := nodeByName(t, "mieru-tcp")
	uriSkip, _, _ := RenderRequest(Request{
		Format: FormatURI, Nodes: []Node{mieru}, ProxyUUID: fixtureUUID, InfoLines: lines,
	})
	decSkip, _ := base64.StdEncoding.DecodeString(string(uriSkip))
	if strings.Contains(string(decSkip), "信息一") {
		t.Error("all skipped: URI should have no info nodes")
	}
	sipSkip, _, _ := RenderRequest(Request{
		Format: FormatSIP008, Nodes: []Node{mieru}, ProxyUUID: fixtureUUID, InfoLines: lines,
	})
	var docSkip sip008Doc
	_ = json.Unmarshal(sipSkip, &docSkip)
	if len(docSkip.Servers) != 0 {
		t.Error("all skipped: SIP008 should have no info nodes")
	}
}

func TestRenderExpiredNewFormats(t *testing.T) {
	notice := "已于 2026-10-07 14:32 到期，续费后更新订阅即可恢复"

	loon, ct := RenderExpired(FormatLoon, notice)
	if ct != "text/plain; charset=utf-8" || !strings.Contains(string(loon), "Shadowsocks,127.0.0.1,1") {
		t.Fatalf("loon: ct=%s body=%s", ct, loon)
	}

	sb, ct := RenderExpired(FormatSurfboard, notice)
	if ct != "text/plain; charset=utf-8" {
		t.Fatalf("surfboard ct: %s", ct)
	}
	sbText := string(sb)
	if !strings.Contains(sbText, "[Proxy]") || !strings.Contains(sbText, "FINAL,节点选择") {
		t.Fatalf("surfboard: %s", sbText)
	}

	sip, ct := RenderExpired(FormatSIP008, notice)
	if ct != "application/json; charset=utf-8" {
		t.Fatalf("sip008 ct: %s", ct)
	}
	var doc sip008Doc
	if err := json.Unmarshal(sip, &doc); err != nil || len(doc.Servers) != 1 || doc.Servers[0].Remarks != notice {
		t.Fatalf("sip008 expired: %v %+v", err, doc)
	}

	stash, ct := RenderExpired(FormatStash, notice)
	clash, ctClash := RenderExpired(FormatClash, notice)
	if ct != "text/yaml; charset=utf-8" || string(stash) != string(clash) {
		t.Fatalf("stash should match clash: stash=%s clash=%s", stash, clash)
	}
	_ = ctClash

	sr, ct := RenderExpired(FormatShadowrocket, notice)
	uri, ctUri := RenderExpired(FormatURI, notice)
	if ct != "text/plain; charset=utf-8" || string(sr) != string(uri) {
		t.Fatalf("shadowrocket should match uri")
	}
	_ = ctUri
}
