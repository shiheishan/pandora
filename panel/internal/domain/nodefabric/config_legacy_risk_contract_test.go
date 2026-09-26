// [INPUT]: 依赖 bootstrapTokenHash、nextLegacyConfigVersion 等校验函数，依赖 platform/sourcetest 按名取旧版发布、回报、下发、入网与生命周期的源码
// [OUTPUT]: 对外提供 TestLegacyConfigPublishRiskReductionContract、TestNodeCreationMaterializesPublishedLegacyConfig、TestAdminPoolValidationFreezesLifecycleState、TestLegacyConfigReportFailsClosedOnAmbiguousIdentity、TestLegacyFetchUsesNeutralNotFoundForRLSMiss、TestLegacyRetirementClosesConfigDelivery、TestLegacyBootstrapLocksAndRejectsTerminalNodes、TestBootstrapTokenIssueValidatesPoolAndPreservesAuditAttribution、TestBootstrapTokenHashBindsTargetName、TestNextLegacyConfigVersion、TestValidateLegacyPublishScope、TestValidateLegacyConfigReport
// [POS]: nodefabric 旧版配置链路的并发与终态契约：发布锁与版本分配、新节点物化、回报身份唯一、RLS 中性 404、退役关闭下发、bootstrap 锁序与终态拒绝
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package nodefabric

import (
	"bytes"
	"math"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestLegacyConfigPublishRiskReductionContract(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	publish := pkg.Decl("Service.PublishConfig")
	// 发布锁在前、发布在后：拼接顺序即原先两者在源码里的先后
	src := pkg.Decls("lockLegacyConfigRelease", "Service.PublishConfig", "nextLegacyConfigVersion", "Service.Bootstrap")
	required := []string{
		`pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`,
		`"node-config-release/"+tenantID`,
		`max(version)::bigint`,
		`bool_or(version <= 0)`,
		`FROM node_configs`,
		`current >= math.MaxInt32`,
		`nextLegacyConfigVersion(current, anomalous)`,
		`lockLegacyConfigRelease(ctx, tx, tenantID)`,
		`syncLegacyDesiredConfigVersion(ctx, tx, tenantID, nodeID)`,
		`UPDATE node_configs SET status='superseded'`,
		`INSERT INTO node_configs`,
	}
	for _, needle := range required {
		if !strings.Contains(src, needle) {
			t.Fatalf("legacy publish safety contract missing %q", needle)
		}
	}
	for _, needle := range []string{
		`AND serving_status<>'retired'`,
		`FOR SHARE`,
	} {
		if !strings.Contains(publish, needle) {
			t.Fatalf("legacy publish lifecycle lock contract missing %q", needle)
		}
	}
	if strings.Contains(pkg.Source(), `FOR KEY SHARE`) {
		t.Fatal("legacy publish target lifecycle is not frozen against concurrent status changes")
	}
	if got := strings.Count(publish, `serving_status<>'retired'`); got < 4 {
		t.Fatalf("retired-node exclusion must cover target validation and every desired update, got %d", got)
	}
	order := pkg.Decls("lockLegacyConfigRelease", "Service.PublishConfig")
	lockAt := strings.Index(order, `pg_catalog.pg_advisory_xact_lock(pg_catalog.hashtextextended($1, 0))`)
	allocateAt := strings.Index(order, `SELECT coalesce(max(version)::bigint, 0),`)
	supersedeAt := strings.Index(order, `UPDATE node_configs SET status='superseded'`)
	insertAt := strings.Index(order, `INSERT INTO node_configs`)
	if lockAt < 0 || allocateAt <= lockAt || supersedeAt <= allocateAt || insertAt <= supersedeAt {
		t.Fatalf("legacy publish lock/allocation/write order drifted: lock=%d allocate=%d supersede=%d insert=%d",
			lockAt, allocateAt, supersedeAt, insertAt)
	}

	if strings.Contains(pkg.Source(), "WHERE tenant_id=$1 AND scope=$2 AND scope_ref IS NOT DISTINCT FROM $3::uuid`,\n\t\t\ttenantID, in.Scope, ref).Scan(&ver)") {
		t.Fatal("per-scope max(version)+1 allocation returned")
	}
}

func TestNodeCreationMaterializesPublishedLegacyConfig(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	serviceSrc := pkg.Decls("Service.Bootstrap", "Service.PublishConfig")
	adminSrc := pkg.Decls("Service.CreateAdminNode", "Service.CloneAdminNode")
	if strings.Count(serviceSrc, `lockLegacyConfigRelease(ctx, tx, tenantID)`) < 2 ||
		!strings.Contains(pkg.Decl("Service.Bootstrap"), `syncLegacyDesiredConfigVersion(ctx, tx, tenantID, nodeID)`) {
		t.Fatal("bootstrap and publication do not share the legacy release lock/materialization domain")
	}
	if strings.Count(adminSrc, `lockLegacyConfigRelease(ctx, tx, tenantID)`) < 2 ||
		!strings.Contains(adminSrc, `syncLegacyDesiredConfigVersion(ctx, tx, tenantID, id)`) ||
		!strings.Contains(adminSrc, `syncLegacyDesiredConfigVersion(ctx, tx, tenantID, cloneID)`) {
		t.Fatal("admin create/clone do not materialize the current applicable config under the release lock")
	}
	for _, needle := range []string{
		`SELECT max(c.version)`,
		`c.status='published'`,
		`c.scope='global'`,
		`c.scope='pool' AND c.scope_ref=n.pool_id`,
		`c.scope='node' AND c.scope_ref=n.id`,
	} {
		if !strings.Contains(pkg.Decl("syncLegacyDesiredConfigVersion"), needle) {
			t.Fatalf("new-node desired materialization contract missing %q", needle)
		}
	}
}

func TestAdminPoolValidationFreezesLifecycleState(t *testing.T) {
	block := sourcetest.Load(t, ".").Decl("validatePool")
	if !strings.Contains(block, `FOR SHARE`) || strings.Contains(block, `FOR KEY SHARE`) {
		t.Fatal("validatePool must hold a SHARE row lock against concurrent disable/delete")
	}
}

func TestLegacyConfigReportFailsClosedOnAmbiguousIdentity(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	src := pkg.Decl("Service.ReportConfigApplied")
	for _, needle := range []string{
		`SELECT pool_id::text FROM nodes`,
		`FOR SHARE`,
		`FROM node_configs AS c`,
		`c.version = $2`,
		`c.published_at IS NOT NULL`,
		`c.status IN ('published','superseded','rolled_back')`,
		`LIMIT 2`,
		`if len(candidates) != 1`,
		`c.scope == "global"`,
		`c.scope == "pool" && poolID != nil && c.ref == *poolID`,
		`c.scope == "node" && c.ref == nodeID`,
		`if !applicable`,
		`httpx.CodeConflict`,
	} {
		if !strings.Contains(src, needle) {
			t.Fatalf("legacy report safety contract missing %q", needle)
		}
	}
	for _, forbidden := range []string{
		`status='published' AND version=$2`,
		`ORDER BY created_at DESC LIMIT 1`,
		`return nil // 配置已被取代，不必记录`,
	} {
		if strings.Contains(pkg.Source(), forbidden) {
			t.Fatalf("legacy report guess/silent-success contract returned: %q", forbidden)
		}
	}
}

func TestLegacyFetchUsesNeutralNotFoundForRLSMiss(t *testing.T) {
	block := sourcetest.Load(t, ".").Decl("Service.FetchConfig")
	for _, needle := range []string{
		`errors.Is(err, pgx.ErrNoRows)`,
		`return nil, httpx.NotFoundOrForbidden()`,
		`!sigExp.After(time.Now())`,
		`SELECT pool_id FROM nodes WHERE tenant_id=$1 AND id=$2`,
		`seenLayers[layerIdentity]`,
		`multiple published configs for logical layer`,
		`global published config has a scope reference`,
	} {
		if !strings.Contains(block, needle) {
			t.Fatalf("FetchConfig neutral RLS miss contract missing %q", needle)
		}
	}
}

func TestLegacyRetirementClosesConfigDelivery(t *testing.T) {
	pkg := sourcetest.Load(t, ".")
	fetchBlock := pkg.Decl("Service.FetchConfig")
	for _, needle := range []string{
		`status NOT IN ('destroyed','retired')`,
		`serving_status<>'retired' FOR SHARE`,
	} {
		if !strings.Contains(fetchBlock, needle) {
			t.Fatalf("retired FetchConfig refusal missing %q", needle)
		}
	}
	identityBlock := pkg.Decl("Service.LookupIdentity")
	for _, needle := range []string{
		`JOIN nodes n ON n.tenant_id=i.tenant_id AND n.id=i.node_id`,
		`n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'`,
	} {
		if !strings.Contains(identityBlock, needle) {
			t.Fatalf("retired identity refusal missing %q", needle)
		}
	}
	block := pkg.Decl("Service.BatchAdminNodeLifecycle")
	for _, needle := range []string{
		`lockLegacyConfigRelease(ctx, tx, tenantID)`,
		`node_identities WHERE tenant_id=$1 AND node_id=$2::uuid AND status='active'`,
		`desired_config_version=CASE WHEN $4='retired' THEN NULL ELSE desired_config_version END`,
	} {
		if !strings.Contains(block, needle) {
			t.Fatalf("retirement release contract missing %q", needle)
		}
	}
	lockAt := strings.Index(block, `lockLegacyConfigRelease(ctx, tx, tenantID)`)
	nodeLockAt := strings.Index(block, `FOR UPDATE OF n`)
	if lockAt < 0 || nodeLockAt <= lockAt {
		t.Fatalf("lifecycle release/node lock order drifted: release=%d node=%d", lockAt, nodeLockAt)
	}
}

func TestLegacyBootstrapLocksAndRejectsTerminalNodes(t *testing.T) {
	// 原窗口从 Bootstrap 到 LookupIdentity，中间只夹着 Identity 类型
	block := sourcetest.Load(t, ".").Decls("Service.Bootstrap", "Identity")
	for _, needle := range []string{
		`lockLegacyConfigRelease(ctx, tx, tenantID)`,
		`bootstrapTokenHash(in.Token, in.NodeName)`,
		`coalesce(node_id::text,'')`,
		`validatePool(ctx, tx, tenantID, *poolID)`,
		`boundNodeID == "" || boundNodeID != nodeID`,
		`tokenPoolID != actualPoolID`,
		`SELECT id,status,serving_status`,
		// 退役/销毁是终态：只有服务器删除级联静默（silenced_by_server_delete=true）
		// 才允许同名重装复活，手工退役必须拒绝 —— 这是 NODE-010 状态机之外的
		// 第二道守卫，契约保持按两个分支分别断言。
		`if servingStatus == "retired" {`,
		`if !silencedByDelete {`,
		`else if nodeStatus == "retired" || nodeStatus == "destroyed" {`,
		`silenced_by_server_delete=false`,
		`SELECT coalesce(max(serial), 0)`,
		`UPDATE node_identities SET status='revoked'`,
		`INSERT INTO node_identities`,
	} {
		if !strings.Contains(block, needle) {
			t.Fatalf("Bootstrap terminal/locking contract missing %q", needle)
		}
	}
	lockAt := strings.Index(block, `lockLegacyConfigRelease(ctx, tx, tenantID)`)
	tokenAt := strings.Index(block, `FROM bootstrap_tokens`)
	nodeAt := strings.Index(block, `SELECT id,status,serving_status`)
	poolAt := strings.Index(block, `validatePool(ctx, tx, tenantID, actualPoolID)`)
	serialAt := strings.Index(block, `SELECT coalesce(max(serial), 0)`)
	revokeAt := strings.Index(block, `UPDATE node_identities SET status='revoked'`)
	insertAt := strings.Index(block, `INSERT INTO node_identities`)
	if lockAt < 0 || tokenAt <= lockAt || nodeAt <= tokenAt || poolAt <= nodeAt || serialAt <= poolAt ||
		revokeAt <= serialAt || insertAt <= revokeAt {
		t.Fatalf("Bootstrap lock/identity order drifted: release=%d token=%d pool=%d node=%d serial=%d revoke=%d insert=%d",
			lockAt, tokenAt, poolAt, nodeAt, serialAt, revokeAt, insertAt)
	}
	if !strings.Contains(block[nodeAt:serialAt], `FOR UPDATE`) {
		t.Fatal("Bootstrap existing-node lookup is not protected by FOR UPDATE")
	}
	if strings.Contains(block, `LEFT JOIN node_identities`) || strings.Contains(block, `GROUP BY n.id`) {
		t.Fatal("Bootstrap still combines lifecycle lock and identity serial lookup")
	}
}

func TestBootstrapTokenIssueValidatesPoolAndPreservesAuditAttribution(t *testing.T) {
	block := sourcetest.Load(t, ".").Decl("Service.IssueBootstrapToken")
	for _, required := range []string{
		`in.PoolID = strings.TrimSpace(in.PoolID)`,
		`validateAdminUUID("pool_id", in.PoolID, false)`,
		`coalesce(pool_id::text,''),`,
		`silenced_by_server_delete FROM nodes`,
		`in.PoolID != "" && in.PoolID != actualPoolID`,
		`effectivePoolID = actualPoolID`,
		`validatePool(ctx, tx, tenantID, effectivePoolID)`,
		`"pool_id": effectivePoolID`,
		`"node_id": auditNodeID`,
	} {
		if !strings.Contains(block, required) {
			t.Fatalf("bootstrap token issue contract missing %q", required)
		}
	}
	nodeAt := strings.Index(block, `SELECT id::text,status,serving_status,coalesce(pool_id::text,''),`)
	poolAt := strings.Index(block, `validatePool(ctx, tx, tenantID, effectivePoolID)`)
	insertAt := strings.Index(block, `INSERT INTO bootstrap_tokens`)
	auditAt := strings.Index(block, `Action: "node.bootstrap_token.issue"`)
	if nodeAt < 0 || poolAt <= nodeAt || insertAt <= poolAt || auditAt <= insertAt {
		t.Fatalf("bootstrap token issue order drifted: pool=%d node=%d insert=%d audit=%d",
			poolAt, nodeAt, insertAt, auditAt)
	}
}

func TestBootstrapTokenHashBindsTargetName(t *testing.T) {
	left := bootstrapTokenHash("same-secret", "node-a")
	right := bootstrapTokenHash("same-secret", "node-b")
	if bytes.Equal(left, right) {
		t.Fatal("bootstrap token hash does not bind the target node name")
	}
	if !bytes.Equal(left, bootstrapTokenHash("same-secret", "node-a")) {
		t.Fatal("bootstrap token hash is not deterministic")
	}
}

func TestNextLegacyConfigVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current int64
		want    int
		wantErr bool
	}{
		{name: "first", current: 0, want: 1},
		{name: "last", current: 2147483646, want: 2147483647},
		{name: "exhausted", current: 2147483647, wantErr: true},
		{name: "negative", current: -1, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextLegacyConfigVersion(tc.current, false)
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("nextLegacyConfigVersion(%d)=(%d,%v), want (%d,error=%t)",
					tc.current, got, err, tc.want, tc.wantErr)
			}
		})
	}
	if _, err := nextLegacyConfigVersion(7, true); err == nil {
		t.Fatal("anomalous legacy version history accepted")
	}
}

func TestValidateLegacyPublishScope(t *testing.T) {
	valid := [][2]string{
		{"global", ""},
		{"pool", "00000000-0000-7000-8000-000000000001"},
		{"node", "00000000-0000-7000-8000-000000000002"},
	}
	for _, tc := range valid {
		if err := validateLegacyPublishScope(tc[0], tc[1]); err != nil {
			t.Fatalf("valid scope rejected: %v: %v", tc, err)
		}
	}
	invalid := [][2]string{
		{"global", "00000000-0000-7000-8000-000000000001"},
		{"pool", ""},
		{"node", "NOT-A-UUID"},
		{"future", ""},
	}
	for _, tc := range invalid {
		if err := validateLegacyPublishScope(tc[0], tc[1]); err == nil {
			t.Fatalf("invalid scope accepted: %v", tc)
		}
	}
}

func TestValidateLegacyConfigReport(t *testing.T) {
	if err := validateLegacyConfigReport(1, "switched", "ok"); err != nil {
		t.Fatalf("valid legacy report rejected: %v", err)
	}
	for _, tc := range []struct {
		version int
		phase   string
		detail  string
	}{
		{version: 0, phase: "switched"},
		{version: 1, phase: "unknown"},
		{version: int(math.MaxInt32) + 1, phase: "failed"},
		{version: 1, phase: "failed", detail: strings.Repeat("x", 2049)},
	} {
		if err := validateLegacyConfigReport(tc.version, tc.phase, tc.detail); err == nil {
			t.Fatalf("invalid legacy report accepted: %+v", tc)
		}
	}
}
