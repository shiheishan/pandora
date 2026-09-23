package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 系统状态：备份、数据库、后台作业。
//
// 做它是因为备份是个彻底的盲区 —— 面板上没有任何地方能看到备份跑没跑。
// 实际情况是它每天都在正常跑、已经攒了十份加密备份，但管理员完全不知道；
// 反过来说，如果哪天定时器坏了，同样没人会发现，直到需要恢复的那一刻。
//
// 对着 xboard 的 getSystemStatus 做的，但内容按我们自己的部署来：它跑在
// Laravel + Horizon 上，关心的是队列和失败作业；我们关心的是备份有没有
// 在跑、异地有没有配、数据库多大。

type backupFile struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"created_at"`
	// HasChecksum 为 false 说明这份备份没有校验文件。verify-backup.sh
	// 缺了它会拒绝验证，等于这份备份不可信。
	HasChecksum bool `json:"has_checksum"`
}

func (h *handlers) systemStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{}

	out["backup"] = h.backupStatus()

	// 数据库体积与连接数：扩容和排查慢查询时最先要看的两个数。
	if err := h.d.Pool.InTx(r.Context(),
		db.Scope{TenantID: httpx.TenantIDFrom(r.Context())},
		func(tx pgx.Tx) error {
			var sizeBytes int64
			var conns, maxConns int
			_ = tx.QueryRow(r.Context(),
				`SELECT pg_database_size(current_database())`).Scan(&sizeBytes)
			_ = tx.QueryRow(r.Context(),
				`SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()`).
				Scan(&conns)
			_ = tx.QueryRow(r.Context(),
				`SELECT setting::int FROM pg_settings WHERE name = 'max_connections'`).
				Scan(&maxConns)
			out["database"] = map[string]any{
				"size_bytes":      sizeBytes,
				"connections":     conns,
				"max_connections": maxConns,
			}
			return nil
		}); err != nil {
		out["database"] = map[string]any{"error": "读取数据库状态失败"}
	}

	httpx.OK(w, out)
}

// backupStatus 直接看文件系统，不依赖任何状态表。
//
// 备份是由 systemd timer 跑脚本产生的，面板并不参与。与其在库里记一份
// 「我以为备份成功了」，不如去看真实产物 —— 定时器停了、磁盘满了、
// 脚本改坏了，这里都会如实反映出来。
func (h *handlers) backupStatus() map[string]any {
	dir := strings.TrimSpace(os.Getenv("AEGIS_BACKUP_DIR"))
	if dir == "" {
		dir = "/var/backups/aegispanel"
	}

	st := map[string]any{"dir": dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// 读不到不等于没备份：目录权限或 systemd 的 ProtectSystem 都可能挡住。
		// 如实说清楚是「看不到」而不是「没有」—— 这两件事的处置完全不同。
		st["readable"] = false
		st["message"] = "面板读不到备份目录（" + dir + "）。" +
			"可能是权限或 systemd 沙箱限制，不代表备份没在跑；" +
			"用 systemctl status aegis-backup.service 直接确认。"
		return st
	}
	st["readable"] = true

	files := []backupFile{}
	checksums := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sha256") {
			checksums[strings.TrimSuffix(e.Name(), ".sha256")] = true
		}
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".dump.age") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		files = append(files, backupFile{
			Name: e.Name(), Size: info.Size(), ModTime: info.ModTime(),
			HasChecksum: checksums[e.Name()],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	st["count"] = len(files)
	st["total_bytes"] = total
	if len(files) > 0 {
		st["latest"] = files[0]
		age := time.Since(files[0].ModTime)
		st["latest_age_hours"] = int(age.Hours())
		// 超过 48 小时没有新备份，多半是定时器出了问题 —— 正常是每天一次。
		st["stale"] = age > 48*time.Hour
		missing := 0
		for _, f := range files {
			if !f.HasChecksum {
				missing++
			}
		}
		st["missing_checksum"] = missing
	} else {
		st["stale"] = true
	}
	if len(files) > 5 {
		st["recent"] = files[:5]
	} else {
		st["recent"] = files
	}

	// 能不能解开：verify-backup.sh 要 AEGIS_BACKUP_AGE_IDENTITY 指向私钥文件。
	//
	// 这一项曾经真的翻过车：备份天天在跑、攒了十份，但这个变量从来没设过，
	// 私钥也不在任何一台机器上 —— 十份备份全是打不开的随机数，而面板上
	// 一切正常。所以只报「加密备份有几份」是不够的，必须同时回答「有没有
	// 人拿得出那把钥匙」。
	//
	// 只看变量有没有设、文件在不在，不读内容。备份私钥不该被一个对外的
	// Web 进程持有，能回答「配了没有」就够了。
	identity := strings.TrimSpace(os.Getenv("AEGIS_BACKUP_AGE_IDENTITY"))
	switch {
	case identity == "":
		st["identity_configured"] = false
		st["identity_hint"] = "没有配置解密私钥（AEGIS_BACKUP_AGE_IDENTITY）。" +
			"备份还在照常加密写入，但没有任何人能解开它们，也跑不了恢复演练。"
	default:
		if _, err := os.Stat(identity); err != nil {
			st["identity_configured"] = false
			st["identity_hint"] = "配置的解密私钥文件读不到：" + identity +
				"。这批备份目前无法验证，也无法恢复。"
		} else {
			st["identity_configured"] = true
		}
	}

	// 异地备份：有没有配 WebDAV。只在本机的备份，机器挂了会跟着一起没。
	offsite := false
	for _, p := range []string{
		"/etc/aegispanel/backup-webdav.json",
		filepath.Join(dir, "backup-webdav.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			offsite = true
			break
		}
	}
	st["offsite_configured"] = offsite

	return st
}
