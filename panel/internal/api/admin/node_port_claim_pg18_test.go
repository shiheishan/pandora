package admin

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/domain/nodefabric"
	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	platformdb "github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// runNodeConfigPG18PortClaimBatch 证明同机端口门禁（w4deliver，用户 2026-10-07 定：先到先得）
// 与节点真实状态的读模型：
//
//  1. 生成列 listen_l4 与 Go 的 nodefabric.ListenL4 逐项一致，唯一部分索引在干净库上建成；
//  2. 并发建两个同服务器、同端口、同 L4 的节点只有一个成功，另一个 409 写明端口；
//  3. 同一端口号 TCP 与 UDP 可以共存，同 UDP 再来一个 409；
//  4. 退役的节点不占端口；
//  5. 改端口撞上别人 409；协议字段原样带回不推进代际，真改了才推进；
//  6. 复制到同一台服务器不换端口 409，给新端口成功；保留端口 22 回 422，443 只提示；
//  7. 绕过门禁的直接写撞唯一索引；
//  8. 节点列表给出运行原因（端口冲突的占用者名字）、生效失败与降级标记。
func runNodeConfigPG18PortClaimBatch(t *testing.T, ctx context.Context, admin *pgx.Conn,
	appPool *platformdb.Pool, signer *platformcrypto.Signer) {
	t.Helper()
	fx := seedNodeConfigPG18Fixture(t, ctx, admin)
	service := nodefabric.NewService(appPool, signer)

	// 1. 生成列与 Go 同口径；索引在干净库上是唯一索引
	cases := []struct{ nodeType, config string }{
		{"vless", `{"network":"tcp"}`}, {"vless", `{"network":"xhttp-h3"}`}, {"vmess", `{"network":"kcp"}`},
		{"trojan", `{"network":"mkcp"}`}, {"trojan", `{"network":"xhttp-h3"}`}, {"hysteria2", `{}`},
		{"tuic", `{}`}, {"juicity", `{}`}, {"shadowsocks", `{"method":"aes-128-gcm"}`},
		{"shadowsocks", `{"network":"udp"}`}, {"mieru", `{"transport":"UDP"}`}, {"mieru", `{"transport":"tcp"}`},
		{"mieru", `{}`}, {"socks", `{"network":"udp"}`}, {"anytls", `{}`}, {"vless", `{"network":" MKCP "}`},
		{"vless", `{"network":5}`}, {"vless", `[]`},
	}
	for i, c := range cases {
		var got string
		if err := admin.QueryRow(ctx, `INSERT INTO nodes(tenant_id,name,status,node_type,protocol_config)
			VALUES($1,$2,'draft',$3,$4::jsonb) RETURNING listen_l4`,
			fx.tenant, "l4-"+fx.suffix+"-"+uuid.NewString()[:8], c.nodeType, c.config).Scan(&got); err != nil {
			t.Fatalf("case %d insert: %v", i, err)
		}
		if want := nodefabric.ListenL4(c.nodeType, json.RawMessage(c.config)); got != want {
			t.Errorf("listen_l4(%s, %s) = %s in SQL, %s in Go", c.nodeType, c.config, got, want)
		}
	}
	var unique bool
	if err := admin.QueryRow(ctx, `SELECT i.indisunique FROM pg_index i
		WHERE i.indexrelid = 'public.nodes_listen_claim_unique'::regclass`).Scan(&unique); err != nil || !unique {
		t.Fatalf("nodes_listen_claim_unique missing or not unique: %v %v", unique, err)
	}

	create := func(name, nodeType string, port int, config string) (*nodefabric.AdminNode, error) {
		return service.CreateAdminNode(ctx, fx.tenant, nodefabric.CreateAdminNodeInput{
			ActorID: fx.actor, Name: name + "-" + fx.suffix, ServerID: fx.server,
			NodeType: nodeType, ServerHost: "claims.example.test", ServerPort: port,
			Kernel: "auto", TrafficRate: 1, ProtocolConfig: json.RawMessage(config),
		})
	}
	const cert = `{"cert_path":"/etc/pandora-native/certs/claims.pem","key_path":"/etc/pandora-native/certs/claims.key"}`
	wantConflict := func(step string, err error, port string) {
		t.Helper()
		var he *httpx.Error
		if !errors.As(err, &he) || he.Code != httpx.CodeConflict || !strings.Contains(he.Message, port) {
			t.Fatalf("%s: err=%v, want 409 naming %s", step, err, port)
		}
	}

	// 2. 并发建两个同端口 TCP 节点
	const port = 21443
	start := make(chan struct{})
	type result struct {
		node *nodefabric.AdminNode
		err  error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, name := range []string{"race-a", "race-b"} {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			<-start
			n, err := create(name, "vless", port, `{}`)
			results <- result{n, err}
		}(name)
	}
	close(start)
	wg.Wait()
	close(results)
	var winner *nodefabric.AdminNode
	var loserErr error
	for r := range results {
		if r.err == nil {
			if winner != nil {
				t.Fatal("two nodes claimed the same server port concurrently")
			}
			winner = r.node
		} else {
			loserErr = r.err
		}
	}
	if winner == nil {
		t.Fatalf("no concurrent create succeeded: %v", loserErr)
	}
	wantConflict("concurrent create", loserErr, "21443/TCP")
	if !strings.Contains(loserErr.Error()+fmtHTTPError(loserErr), winner.Name) {
		t.Fatalf("conflict %q does not name the holder %q", fmtHTTPError(loserErr), winner.Name)
	}

	// 3. 同端口 UDP 可以共存；再来一个 UDP 409
	udp, err := create("udp-a", "hysteria2", port, cert)
	if err != nil {
		t.Fatalf("UDP on a TCP-claimed port number was refused: %v", err)
	}
	_, err = create("udp-b", "tuic", port, cert)
	wantConflict("second UDP", err, "21443/UDP")

	// 4. 退役的节点不占端口
	if _, err := admin.Exec(ctx, `UPDATE nodes SET serving_status='retired' WHERE id=$1`, winner.ID); err != nil {
		t.Fatal(err)
	}
	reuse, err := create("reuse", "shadowsocks", port, `{"method":"aes-256-gcm"}`)
	if err != nil {
		t.Fatalf("a retired node still holds its port: %v", err)
	}

	// 5. 改端口撞上别人；原样带回协议字段不推进代际
	mover, err := create("mover", "vless", port+1, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	clash := port
	_, err = service.PatchAdminNode(ctx, fx.tenant, mover.ID, nodefabric.PatchAdminNodeInput{
		ActorID: fx.actor, RowVersion: mover.RowVersion, ServerPort: &clash})
	wantConflict("patch onto a held port", err, "21443/TCP")
	generation := func(id string) (g int64) {
		t.Helper()
		if err := admin.QueryRow(ctx, `SELECT config_source_generation FROM nodes WHERE id=$1`, id).Scan(&g); err != nil {
			t.Fatal(err)
		}
		return g
	}
	before := generation(mover.ID)
	same := port + 1
	host, nodeType, kernel := "claims.example.test", "vless", "auto"
	raw := json.RawMessage(`{ }`)
	patched, err := service.PatchAdminNode(ctx, fx.tenant, mover.ID, nodefabric.PatchAdminNodeInput{
		ActorID: fx.actor, RowVersion: mover.RowVersion, ServerPort: &same, ServerHost: &host,
		NodeType: &nodeType, Kernel: &kernel, ProtocolConfig: &raw})
	if err != nil {
		t.Fatalf("patch with unchanged protocol: %v", err)
	}
	if got := generation(mover.ID); got != before {
		t.Fatalf("unchanged protocol fields bumped config_source_generation %d → %d", before, got)
	}
	moved := port + 2
	if _, err := service.PatchAdminNode(ctx, fx.tenant, mover.ID, nodefabric.PatchAdminNodeInput{
		ActorID: fx.actor, RowVersion: patched.RowVersion, ServerPort: &moved}); err != nil {
		t.Fatalf("patch to a free port: %v", err)
	}
	if got := generation(mover.ID); got != before+1 {
		t.Fatalf("real port change: generation %d → %d, want +1", before, got)
	}

	// 6. 复制到同一台服务器不换端口 409，换端口成功；保留端口
	_, err = service.CloneAdminNode(ctx, fx.tenant, reuse.ID, nodefabric.CloneAdminNodeInput{
		ActorID: fx.actor, RowVersion: reuse.RowVersion, Name: "reuse-copy-" + fx.suffix})
	wantConflict("copy onto the same port", err, "21443/TCP")
	copyPort := port + 3
	if _, err := service.CloneAdminNode(ctx, fx.tenant, reuse.ID, nodefabric.CloneAdminNodeInput{
		ActorID: fx.actor, RowVersion: reuse.RowVersion, Name: "reuse-copy-" + fx.suffix, ServerPort: &copyPort}); err != nil {
		t.Fatalf("copy with a new port: %v", err)
	}
	_, err = create("ssh", "vless", 22, `{}`)
	var he *httpx.Error
	if !errors.As(err, &he) || he.Code != httpx.CodeValidationFailed || he.Fields["server_port"] == "" {
		t.Fatalf("reserved port 22: err=%v, want 422 on server_port", err)
	}
	https, err := create("https", "vless", 443, `{}`)
	if err != nil || len(https.Warnings) == 0 {
		t.Fatalf("panel port 443 on an unknown host: node=%v err=%v, want created with a warning", https, err)
	}

	// 7. 绕过门禁的直接写撞唯一索引（只有夹具连接能这么写）
	_, err = admin.Exec(ctx, `INSERT INTO nodes(tenant_id,name,status,serving_status,server_id,server_port,node_type,protocol_config)
		VALUES($1,$2,'draft','draft',$3,$4,'tuic','{}')`, fx.tenant, "bypass-"+fx.suffix, fx.server, port)
	if !platformdb.IsUniqueViolation(err) || platformdb.ConstraintName(err) != "nodes_listen_claim_unique" {
		t.Fatalf("direct conflicting insert: %v, want nodes_listen_claim_unique violation", err)
	}

	// 8. 列表的运行状态：端口冲突原因解析出占用者名字；生效失败；降级
	if _, err := admin.Exec(ctx, `UPDATE nodes SET runtime_status='degraded',
		runtime_reason='port_in_use:21443/udp:'||$2, runtime_state_at=now() WHERE id=$1`, reuse.ID, udp.ID); err != nil {
		t.Fatal(err)
	}
	release := uuid.NewString()
	hash := strings.Repeat("ab", 32)
	if _, err := admin.Exec(ctx, `INSERT INTO node_effective_config_releases
		(id,tenant_id,node_id,generation,payload,content_hash,source_manifest,source_manifest_hash,key_id)
		VALUES($1,$2,$3,7,'{}',decode($4,'hex'),'{}',decode($4,'hex'),'AAAAAAAAAAA')`,
		release, fx.tenant, udp.ID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE nodes SET desired_effective_release_id=$2, desired_effective_generation=7
		WHERE id=$1`, udp.ID, release); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO node_config_applications
		(tenant_id,node_id,effective_release_id,effective_generation,effective_content_hash,report_id,phase,detail)
		VALUES($1,$2,$3,7,decode($4,'hex'),gen_random_uuid(),'failed','{"contract":"x","message":"bind: address already in use"}')`,
		fx.tenant, udp.ID, release, hash); err != nil {
		t.Fatal(err)
	}
	rows, _, err := service.QueryAdminNodes(ctx, fx.tenant, nodefabric.AdminNodeQuery{Limit: 1000, IncludeRetired: true})
	if err != nil {
		t.Fatalf("node list: %v", err)
	}
	byID := map[string]nodefabric.AdminNodeListRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	r := byID[reuse.ID]
	if r.RuntimeReasonNode == nil || *r.RuntimeReasonNode != udp.Name || !r.DeliveryDegraded {
		t.Fatalf("port_in_use reason: holder=%v degraded=%v, want %q and degraded", r.RuntimeReasonNode, r.DeliveryDegraded, udp.Name)
	}
	f := byID[udp.ID]
	if f.EffectiveState != nodefabric.EffectiveFailed || f.LastApplyFailure == nil ||
		f.LastApplyFailure.Detail != "bind: address already in use" || !f.DeliveryDegraded ||
		f.DesiredEffectiveGeneration == nil || *f.DesiredEffectiveGeneration != 7 {
		t.Fatalf("failed release: state=%q failure=%+v degraded=%v desired=%v",
			f.EffectiveState, f.LastApplyFailure, f.DeliveryDegraded, f.DesiredEffectiveGeneration)
	}
	if m := byID[mover.ID]; m.EffectiveState != nodefabric.EffectiveNone || m.DeliveryDegraded || m.PortConflictNode != nil {
		t.Fatalf("plain draft node: state=%q degraded=%v conflict=%v", m.EffectiveState, m.DeliveryDegraded, m.PortConflictNode)
	}
}

func fmtHTTPError(err error) string {
	var he *httpx.Error
	if errors.As(err, &he) {
		return he.Message
	}
	return ""
}

// nodeConfigPG18Port 给复制节点另配端口：副本复制到同一台服务器时不能沿用原节点端口
// （同机端口门禁，00122），node_config 各批的复制用例都经它取。
func nodeConfigPG18Port(port int) *int { return &port }
