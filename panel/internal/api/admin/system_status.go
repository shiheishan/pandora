package admin

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aegispanel/aegis/internal/platform/config"
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
	// HasSeal 为 false 说明这份本地备份没有封条（<归档>.seal）：verify-backup.sh 与 restore-postgres.sh 都拒绝它，
	// 等于恢复不了，不算有效备份
	HasSeal bool `json:"has_seal"`
}

// systemStatusResponse 是 GET v1/system/status 的响应。
type systemStatusResponse struct {
	Backup     backupStatusView  `json:"backup"`
	Database   databaseStatus    `json:"database"`
	State      string            `json:"state"`
	Components []systemComponent `json:"components"`
}

// databaseStatus 是数据库统计：读失败时只有 error，读到了只有三个数（R52：不拿零值冒充）。
type databaseStatus struct {
	Error          string `json:"error,omitempty"`
	SizeBytes      *int64 `json:"size_bytes,omitempty"`
	Connections    *int   `json:"connections,omitempty"`
	MaxConnections *int   `json:"max_connections,omitempty"`
}

// metrics 把读到的统计摊成 postgres 组件要的键值，缺的键不出现。
func (d databaseStatus) metrics() map[string]any {
	m := map[string]any{}
	if d.SizeBytes != nil {
		m["size_bytes"] = *d.SizeBytes
	}
	if d.Connections != nil {
		m["connections"] = *d.Connections
	}
	if d.MaxConnections != nil {
		m["max_connections"] = *d.MaxConnections
	}
	return m
}

// backupStatusView 是备份目录的探测结果。读不到目录时只有 dir / readable / message；
// 读得到时 message 缺席，latest / latest_age_hours / missing_checksum 只在有备份文件时出现，
// identity_hint 只在私钥没配好时出现。指针字段 nil 即键缺省。
type backupStatusView struct {
	Dir                string        `json:"dir"`
	Readable           bool          `json:"readable"`
	Message            string        `json:"message,omitempty"`
	Count              *int          `json:"count,omitempty"`
	TotalBytes         *int64        `json:"total_bytes,omitempty"`
	Latest             *backupFile   `json:"latest,omitempty"`
	LatestAgeHours     *int          `json:"latest_age_hours,omitempty"`
	Stale              *bool         `json:"stale,omitempty"`
	MissingChecksum    *int          `json:"missing_checksum,omitempty"`
	MissingSeal        *int          `json:"missing_seal,omitempty"`
	Recent             *[]backupFile `json:"recent,omitempty"`
	IdentityConfigured *bool         `json:"identity_configured,omitempty"`
	IdentityHint       string        `json:"identity_hint,omitempty"`
	OffsiteConfigured  *bool         `json:"offsite_configured,omitempty"`
}

func statusPtr[T any](v T) *T { return &v }

func (h *handlers) systemStatus(w http.ResponseWriter, r *http.Request) {
	backup := h.backupStatus()

	// 数据库体积与连接数：扩容和排查慢查询时最先要看的两个数。
	// 三个数要么一起读到，要么一个都不给：读失败时不能拿零值冒充（R52）。
	database := databaseStatus{Error: "读取数据库状态失败"}
	if stats, err := h.d.Ops.DatabaseStats(r.Context(), httpx.TenantIDFrom(r.Context())); err != nil {
		h.d.Log.Warn("读取数据库统计失败", "err", err)
	} else {
		database = databaseStatus{
			SizeBytes:      statusPtr(stats.SizeBytes),
			Connections:    statusPtr(stats.Connections),
			MaxConnections: statusPtr(stats.MaxConnections),
		}
	}
	state, components := h.systemComponents(r, database.metrics(), backup)

	httpx.OK(w, systemStatusResponse{Backup: backup, Database: database, State: state, Components: components})
}

// backupStatus 直接看文件系统，不依赖任何状态表。
//
// 备份是由 systemd timer 跑脚本产生的，面板并不参与。与其在库里记一份
// 「我以为备份成功了」，不如去看真实产物 —— 定时器停了、磁盘满了、
// 脚本改坏了，这里都会如实反映出来。
func (h *handlers) backupStatus() backupStatusView {
	dir := h.d.Cfg.BackupDir

	st := backupStatusView{Dir: dir}

	entries, err := os.ReadDir(dir)
	if err != nil {
		// 读不到不等于没备份：目录权限或 systemd 的 ProtectSystem 都可能挡住。
		// 如实说清楚是「看不到」而不是「没有」—— 这两件事的处置完全不同。
		st.Readable = false
		st.Message = "面板读不到备份目录（" + dir + "）。" +
			"可能是权限或 systemd 沙箱限制，不代表备份没在跑；" +
			"用 systemctl status aegis-backup.service 直接确认。"
		return st
	}
	st.Readable = true

	files := []backupFile{}
	checksums := map[string]bool{}
	seals := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".sha256") {
			checksums[strings.TrimSuffix(e.Name(), ".sha256")] = true
		}
		if strings.HasSuffix(e.Name(), ".seal") {
			seals[strings.TrimSuffix(e.Name(), ".seal")] = true
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
			HasChecksum: checksums[e.Name()], HasSeal: seals[e.Name()],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	st.Count = statusPtr(len(files))
	st.TotalBytes = statusPtr(total)
	// 「最新备份」只算恢复得了的那份：有校验文件、有封条。封不上的（私钥读不了等）留在目录里也不算，
	// 不然备份天天失败在封条这一步，概览却一直显示「最新备份有效」
	var latest *backupFile
	for i := range files {
		if files[i].HasChecksum && files[i].HasSeal {
			latest = &files[i]
			break
		}
	}
	if len(files) > 0 {
		missing, unsealed := 0, 0
		for _, f := range files {
			if !f.HasChecksum {
				missing++
			}
			if !f.HasSeal {
				unsealed++
			}
		}
		st.MissingChecksum = statusPtr(missing)
		st.MissingSeal = statusPtr(unsealed)
	}
	if latest != nil {
		st.Latest = statusPtr(*latest)
		age := time.Since(latest.ModTime)
		st.LatestAgeHours = statusPtr(int(age.Hours()))
		// 超过 48 小时没有新备份，多半是定时器出了问题 —— 正常是每天一次。
		st.Stale = statusPtr(age > 48*time.Hour)
	} else {
		st.Stale = statusPtr(true)
	}
	if len(files) > 5 {
		st.Recent = statusPtr(files[:5])
	} else {
		st.Recent = statusPtr(files)
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
	identity := h.d.Cfg.BackupAgeIdentity
	switch {
	case identity == "":
		st.IdentityConfigured = statusPtr(false)
		st.IdentityHint = "没有配置解密私钥（AEGIS_BACKUP_AGE_IDENTITY）。" +
			"备份还在照常加密写入，但没有任何人能解开它们，也跑不了恢复演练。"
	default:
		if _, err := os.Stat(identity); err != nil {
			st.IdentityConfigured = statusPtr(false)
			st.IdentityHint = "配置的解密私钥文件读不到：" + identity +
				"。这批备份目前无法验证，也无法恢复。"
		} else {
			st.IdentityConfigured = statusPtr(true)
		}
	}

	// 异地备份：有没有配 WebDAV。只在本机的备份，机器挂了会跟着一起没。
	offsite := false
	for _, p := range []string{
		config.DefaultBackupWebDAVConfigPath,
		filepath.Join(dir, "backup-webdav.json"),
	} {
		if _, err := os.Stat(p); err == nil {
			offsite = true
			break
		}
	}
	st.OffsiteConfigured = statusPtr(offsite)

	return st
}
