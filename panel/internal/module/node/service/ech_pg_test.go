package service

import (
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

const echPGTrojanTLS = `{"network":"tcp","tls":1,"cert_path":"/etc/x/fullchain.pem","key_path":"/etc/x/privkey.pem","server_name":"node.example.com"}`

func TestNodeECHPG(t *testing.T) {
	env, ctx := newInvEnv(t)
	srv := env.readyServer(t, ctx, "ech-srv")
	ss := env.ssNode(t, ctx, srv.ID, "ech-ss", 8388)
	trojan := env.node(t, ctx, srv.ID, "ech-trojan", "trojan", 8443, echPGTrojanTLS)

	_, err := env.svc.GenerateNodeECH(ctx, ss.ID, "ech.example.com")
	_ = invWantCode(t, err, errs.Conflict)

	st, err := env.svc.GetNodeECH(ctx, ss.ID)
	if err != nil {
		t.Fatalf("GetNodeECH ss: %v", err)
	}
	if st.Supported || st.UnsupportedReason == "" {
		t.Fatalf("ss ECH: supported=%v reason=%q", st.Supported, st.UnsupportedReason)
	}

	ech1, err := env.svc.GenerateNodeECH(ctx, trojan.ID, "ech.example.com")
	if err != nil {
		t.Fatalf("GenerateNodeECH: %v", err)
	}
	if !ech1.Enabled || ech1.PublicName != "ech.example.com" {
		t.Fatalf("ech1: %+v", ech1)
	}
	if !strings.HasPrefix(ech1.ConfigPEM, "-----BEGIN ECH CONFIGS-----") {
		t.Fatalf("ConfigPEM: %q", ech1.ConfigPEM)
	}

	var sealed string
	if err := env.row(ctx, `SELECT encode(private_key_sealed, 'escape') FROM node_ech_keys WHERE node_id = ? AND superseded_at IS NULL`, trojan.ID).Scan(&sealed); err != nil {
		t.Fatalf("读封存私钥: %v", err)
	}
	if !strings.HasPrefix(sealed, "sealed:node_ech_key:") {
		t.Fatalf("sealed 格式: %q", sealed)
	}
	// fakeSealer 在 AAD 之后仍带明文便于测 AAD，但必须以 sealed: 前缀封存，不能裸存 32 字节
	if len(sealed) < len("sealed:node_ech_key:")+36+1+32 {
		t.Fatalf("sealed 过短: %d", len(sealed))
	}

	var auditN int
	if err := env.row(ctx, `SELECT count(*) FROM audit_events WHERE resource_type = 'node' AND resource_id = ? AND action = 'node.ech_generate'`, trojan.ID).Scan(&auditN); err != nil {
		t.Fatalf("审计: %v", err)
	}
	if auditN != 1 {
		t.Fatalf("audit count=%d want 1", auditN)
	}

	ech2, err := env.svc.GenerateNodeECH(ctx, trojan.ID, "")
	if err != nil {
		t.Fatalf("轮换: %v", err)
	}
	if ech2.RetiredKeys != 1 || ech2.PublicName != "ech.example.com" {
		t.Fatalf("ech2: %+v", ech2)
	}

	docKeys, bad, err := env.svc.echDocKeys(ctx, []uuid.UUID{trojan.ID})
	if err != nil {
		t.Fatalf("echDocKeys: %v", err)
	}
	if len(bad) != 0 {
		t.Fatalf("bad=%v", bad)
	}
	keys := docKeys[trojan.ID]
	if len(keys) != 2 {
		t.Fatalf("doc keys=%d want 2", len(keys))
	}
	if !keys[0].SendAsRetry || keys[1].SendAsRetry {
		t.Fatalf("SendAsRetry: %v %v", keys[0].SendAsRetry, keys[1].SendAsRetry)
	}

	if err := env.exec(ctx, `UPDATE node_ech_keys SET superseded_at = now() - interval '8 days' WHERE node_id = ? AND superseded_at IS NOT NULL`, trojan.ID); err != nil {
		t.Fatal(err)
	}
	n, err := env.svc.PurgeRetiredECHKeys(ctx)
	if err != nil || n != 1 {
		t.Fatalf("PurgeRetiredECHKeys: n=%d err=%v", n, err)
	}
	ech3, err := env.svc.GetNodeECH(ctx, trojan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if ech3.RetiredKeys != 0 {
		t.Fatalf("RetiredKeys=%d want 0", ech3.RetiredKeys)
	}

	if err := env.svc.DisableNodeECH(ctx, trojan.ID); err != nil {
		t.Fatalf("DisableNodeECH: %v", err)
	}
	dis, err := env.svc.GetNodeECH(ctx, trojan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if dis.Enabled {
		t.Fatal("应已停用")
	}
	var rowN int
	if err := env.row(ctx, `SELECT count(*) FROM node_ech_keys WHERE node_id = ?`, trojan.ID).Scan(&rowN); err != nil {
		t.Fatal(err)
	}
	if rowN != 0 {
		t.Fatalf("keys left=%d", rowN)
	}
	if err := env.svc.DisableNodeECH(ctx, trojan.ID); err != nil {
		t.Fatalf("再禁一次: %v", err)
	}

	_, err = env.svc.GetNodeECH(ctx, uuid.New())
	_ = invWantCode(t, err, errs.NotFound)

	_, err = env.svc.GenerateNodeECH(ctx, trojan.ID, "192.0.2.1")
	e := invWantCode(t, err, errs.ValidationFailed)
	if e.Fields["public_name"] == "" {
		t.Fatalf("fields=%v", e.Fields)
	}
}
