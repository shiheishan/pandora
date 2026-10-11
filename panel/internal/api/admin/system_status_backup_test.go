package admin

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/config"
)

// 备份概览只把「有校验文件、有封条」的那份算作有效的最新备份：封不上的备份恢复不了（verify-backup.sh 拒绝），
// 不能让它看起来是好的；缺封条的份数单独报，组件转 warn
func TestBackupStatusCountsOnlySealedBackups(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, age time.Duration) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(p, when, when); err != nil {
			t.Fatal(err)
		}
	}
	// 较早的一份：完整（归档、校验、封条）
	write("aegis-postgres-20261009T011540Z.dump.age", 30*time.Hour)
	write("aegis-postgres-20261009T011540Z.dump.age.sha256", 30*time.Hour)
	write("aegis-postgres-20261009T011540Z.dump.age.seal", 30*time.Hour)
	// 最新的一份：封条没写成
	write("aegis-postgres-20261010T011540Z.dump.age", time.Hour)
	write("aegis-postgres-20261010T011540Z.dump.age.sha256", time.Hour)
	// 其余条件都满足（私钥在、配了异地备份、不过期），组件的 warn 只能来自缺封条
	keyDir := t.TempDir()
	identity := filepath.Join(keyDir, "backup-age.key")
	if err := os.WriteFile(identity, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "backup-webdav.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.BackupDir = dir
	cfg.BackupAgeIdentity = identity
	h := &handlers{d: Deps{Cfg: cfg}}

	st := h.backupStatus()
	if st.Latest == nil || st.Latest.Name != "aegis-postgres-20261009T011540Z.dump.age" || !st.Latest.HasSeal {
		t.Fatalf("latest valid backup = %+v, want the sealed one from 30 hours ago", st.Latest)
	}
	if st.LatestAgeHours == nil || *st.LatestAgeHours < 29 {
		t.Fatalf("latest age must come from the sealed backup: %v", st.LatestAgeHours)
	}
	if st.MissingSeal == nil || *st.MissingSeal != 1 {
		t.Fatalf("missing seal = %v, want 1", st.MissingSeal)
	}
	if c := backupComponent(st); c.State != "warn" {
		t.Fatalf("an unsealed backup must turn the backup component to warn: %+v", c)
	}
	// 撤掉那份没封条的，其余都齐：组件回到 ok
	for _, n := range []string{"aegis-postgres-20261010T011540Z.dump.age", "aegis-postgres-20261010T011540Z.dump.age.sha256"} {
		if err := os.Rename(filepath.Join(dir, n), filepath.Join(keyDir, n)); err != nil {
			t.Fatal(err)
		}
	}
	if c := backupComponent(h.backupStatus()); c.State != "ok" {
		t.Fatalf("with every backup sealed the component must be ok: %+v", c)
	}
	for _, n := range []string{"aegis-postgres-20261010T011540Z.dump.age", "aegis-postgres-20261010T011540Z.dump.age.sha256"} {
		if err := os.Rename(filepath.Join(keyDir, n), filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	// 一份封条都没有：没有有效的最新备份，算过期
	for _, n := range []string{"aegis-postgres-20261009T011540Z.dump.age.seal"} {
		if err := os.Remove(filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
	st = h.backupStatus()
	if st.Latest != nil || st.Stale == nil || !*st.Stale {
		t.Fatalf("without any sealed backup there is no valid latest one: latest=%+v stale=%v", st.Latest, st.Stale)
	}
}
