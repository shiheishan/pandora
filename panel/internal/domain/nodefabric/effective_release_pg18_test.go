package nodefabric

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	platformcrypto "github.com/aegispanel/aegis/internal/platform/crypto"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const effectivePG18Fixture = "disposable-v1"

func TestEffectiveReleasePG18(t *testing.T) {
	if strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_FIXTURE")) != effectivePG18Fixture {
		t.Skip("AEGIS_EFFECTIVE_PG18_FIXTURE is not disposable-v1")
	}
	appDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DSN"))
	adminDSN := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_ADMIN_DSN"))
	expectedDB := strings.TrimSpace(os.Getenv("AEGIS_EFFECTIVE_PG18_DATABASE"))
	if appDSN == "" || adminDSN == "" || expectedDB == "" {
		t.Skip("effective PG18 DSNs and expected database are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var dbName string
	var serverVersion int
	if err := admin.QueryRow(ctx, `SELECT current_database(), current_setting('server_version_num')::int`).Scan(&dbName, &serverVersion); err != nil {
		t.Fatal(err)
	}
	if dbName != expectedDB || serverVersion < 180000 || serverVersion >= 190000 {
		t.Fatalf("refusing unexpected PG target: database=%q version=%d", dbName, serverVersion)
	}
	app, err := db.Open(ctx, appDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	signer, err := platformcrypto.NewSigner([]byte("effective-pg18-test-seed-32-byte"))
	if err != nil {
		t.Fatal(err)
	}
	svc := NewService(app, signer)

	tenantA, tenantB := uuid.New(), uuid.New()
	nodeA, nodeB := uuid.New(), uuid.New()
	for _, row := range []struct {
		tenant uuid.UUID
		node   uuid.UUID
		label  string
	}{{tenantA, nodeA, "a"}, {tenantB, nodeB, "b"}} {
		if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`, row.tenant, "effective-"+row.label+"-"+uuid.NewString(), "Effective "+row.label); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO nodes(id,tenant_id,name,status) VALUES($1,$2,$3,'active')`, row.node, row.tenant, "node-"+row.label); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("nonce_claim_is_atomic_and_tenant_scoped", func(t *testing.T) {
		nonce := []byte("0123456789abcdef")
		fingerprint := sha256.Sum256([]byte("request-a"))
		requestTS := time.Now().UTC().Add(4 * time.Minute)
		const workers = 12
		start := make(chan struct{})
		results := make(chan error, workers)
		var wg sync.WaitGroup
		for i := 0; i < workers; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				results <- svc.ClaimSignedRequest(ctx, tenantA.String(), nodeA.String(), nonce, fingerprint[:], requestTS)
			}()
		}
		close(start)
		wg.Wait()
		close(results)
		success := 0
		var failures []string
		for err := range results {
			if err == nil {
				success++
			} else {
				failures = append(failures, err.Error())
			}
		}
		if success != 1 {
			t.Fatalf("same nonce succeeded %d times; want 1; failures=%v", success, failures)
		}

		fingerprintB := sha256.Sum256([]byte("request-b"))
		if err := svc.ClaimSignedRequest(ctx, tenantB.String(), nodeB.String(), nonce, fingerprintB[:], requestTS); err != nil {
			t.Fatalf("same nonce for another tenant/node was rejected: %v", err)
		}
		var expires time.Time
		if err := admin.QueryRow(ctx, `SELECT expires_at FROM node_request_nonces WHERE tenant_id=$1 AND node_id=$2 AND nonce=$3`, tenantA, nodeA, nonce).Scan(&expires); err != nil {
			t.Fatal(err)
		}
		if expires.Before(requestTS.Add(5*time.Minute)) || expires.Before(time.Now().Add(10*time.Minute)) {
			t.Fatalf("future request nonce expires too early: request=%s expiry=%s", requestTS, expires)
		}
		var visibleB int
		if err := app.InTx(ctx, db.Scope{TenantID: tenantA.String()}, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM node_request_nonces WHERE tenant_id=$1`, tenantB).Scan(&visibleB)
		}); err != nil {
			t.Fatal(err)
		}
		if visibleB != 0 {
			t.Fatalf("tenant A saw %d tenant B nonce rows", visibleB)
		}
	})

	t.Run("effective_report_order_identity_and_late_release", func(t *testing.T) {
		hash1 := sha256.Sum256([]byte("release-1"))
		hash2 := sha256.Sum256([]byte("release-2"))
		release1, release2 := uuid.New(), uuid.New()
		insertRelease := func(id uuid.UUID, generation int64, hash [32]byte) {
			t.Helper()
			_, err := admin.Exec(ctx, `INSERT INTO node_effective_config_releases
				(id,tenant_id,node_id,generation,payload,content_hash,source_manifest,source_manifest_hash,key_id)
				VALUES($1,$2,$3,$4,'{}',$5,'{}',$6,'AAAAAAAAAAA')`, id, tenantA, nodeA, generation, hash[:], hash[:])
			if err != nil {
				t.Fatal(err)
			}
		}
		insertRelease(release1, 1, hash1)
		insertRelease(release2, 2, hash2)
		if _, err := admin.Exec(ctx, `UPDATE nodes SET desired_effective_release_id=$1,desired_effective_generation=1 WHERE tenant_id=$2 AND id=$3`, release1, tenantA, nodeA); err != nil {
			t.Fatal(err)
		}
		report := func(id uuid.UUID, release uuid.UUID, generation uint64, hash [32]byte, phase string) error {
			return svc.ReportEffectiveConfigApplied(ctx, tenantA.String(), nodeA.String(), EffectiveConfigReportInput{
				ReportID: id.String(), ReleaseID: release.String(), Generation: generation,
				ContentHash: base64.StdEncoding.EncodeToString(hash[:]), Phase: phase,
			})
		}
		if err := report(uuid.New(), release1, 1, hash1, "health_passed"); err == nil {
			t.Fatal("health_passed without switched evidence succeeded")
		}
		switchedID, healthID := uuid.New(), uuid.New()
		if err := report(switchedID, release1, 1, hash1, "switched"); err != nil {
			t.Fatal(err)
		}
		if err := report(healthID, release1, 1, hash1, "health_passed"); err != nil {
			t.Fatal(err)
		}
		if err := report(healthID, release1, 1, hash1, "health_passed"); err != nil {
			t.Fatalf("idempotent health report failed: %v", err)
		}
		var count int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM node_config_applications WHERE tenant_id=$1 AND node_id=$2 AND report_id=$3`, tenantA, nodeA, healthID).Scan(&count); err != nil || count != 1 {
			t.Fatalf("idempotent report row count=%d err=%v", count, err)
		}
		if _, err := admin.Exec(ctx, `UPDATE nodes SET desired_effective_release_id=$1,desired_effective_generation=2 WHERE tenant_id=$2 AND id=$3`, release2, tenantA, nodeA); err != nil {
			t.Fatal(err)
		}
		if err := report(uuid.New(), release1, 1, hash1, "health_passed"); err == nil {
			t.Fatal("late old release health report replaced newer desired state")
		}
		var applied string
		var generation int64
		if err := admin.QueryRow(ctx, `SELECT applied_effective_release_id::text,applied_effective_generation FROM nodes WHERE tenant_id=$1 AND id=$2`, tenantA, nodeA).Scan(&applied, &generation); err != nil {
			t.Fatal(err)
		}
		if applied != release1.String() || generation != 1 {
			t.Fatalf("last healthy applied identity changed: %s/%d", applied, generation)
		}
		cross := EffectiveConfigReportInput{ReportID: uuid.NewString(), ReleaseID: release1.String(), Generation: 1, ContentHash: base64.StdEncoding.EncodeToString(hash1[:]), Phase: "switched"}
		if err := svc.ReportEffectiveConfigApplied(ctx, tenantB.String(), nodeB.String(), cross); err == nil {
			t.Fatal("cross-node effective release report succeeded")
		}
	})

	t.Run("existing_release_refetch_does_not_revalidate_expired_sources", func(t *testing.T) {
		tenant, node, release := uuid.New(), uuid.New(), uuid.New()
		if _, err := admin.Exec(ctx, `INSERT INTO tenants(id,slug,display_name) VALUES($1,$2,$3)`,
			tenant, "effective-refetch-"+uuid.NewString(), "Effective refetch"); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `INSERT INTO nodes
			(id,tenant_id,name,status,serving_status,node_type,server_host,server_port,kernel,protocol_config)
			VALUES($1,$2,'refetch','active','active','vless','127.0.0.1',443,'auto','{}')`, node, tenant); err != nil {
			t.Fatal(err)
		}
		payload := []byte(`{"protocol":"vless","server_port":443}`)
		manifest := []byte(`{"layers":[{"id":"expired-source"}]}`)
		contentHash, manifestHash := sha256.Sum256(payload), sha256.Sum256(manifest)
		if _, err := admin.Exec(ctx, `INSERT INTO node_effective_config_releases
			(id,tenant_id,node_id,generation,payload,content_hash,source_manifest,source_manifest_hash,key_id)
			VALUES($1,$2,$3,1,$4,$5,$6,$7,$8)`, release, tenant, node, payload, contentHash[:],
			manifest, manifestHash[:], svc.signer.KeyID()); err != nil {
			t.Fatal(err)
		}
		cfg, err := svc.FetchEffectiveConfig(ctx, tenant.String(), node.String())
		if err != nil {
			t.Fatalf("existing immutable release was not reusable: %v", err)
		}
		if cfg.ReleaseID != release.String() || cfg.Generation != 1 || cfg.KeyID != svc.signer.KeyID() {
			t.Fatalf("unexpected reused release identity: %#v", cfg)
		}
	})
}
