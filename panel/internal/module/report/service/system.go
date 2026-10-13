package service

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	notifysvc "github.com/aegispanel/aegis/internal/module/notify/service"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
)

// 备份判定。
const (
	backupStaleAfter = 48 * time.Hour // 正常每天一次，超过 48 小时没有新备份多半是定时器坏了
	backupRecent     = 5
	probeTimeout     = 5 * time.Second
)

// offsiteConfigPaths 是异地备份（WebDAV）配置可能在的位置；备份目录里的 backup-webdav.json 另算。单测可替换。
var offsiteConfigPaths = []string{"/etc/aegispanel/backup-webdav.json"}

// SystemStatus 逐项探测数据库、Redis、节点、支付回调、邮件与 Telegram 投递、实时推送、备份。
// 任何一项失败只把那一项标成 down / unknown，不让整个接口报错：系统状态页恰恰要在部分组件坏掉时还能打开。
func (s *Service) SystemStatus(ctx context.Context) (SystemStatus, error) {
	return cached(ctx, s, "report:v1:system:status", func(ctx context.Context) (SystemStatus, error) {
		return s.loadSystemStatus(ctx)
	})
}

func (s *Service) loadSystemStatus(ctx context.Context) (SystemStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	now := s.now()

	backup := s.probeBackup(now)

	var db *DatabaseStats
	dbMetrics := map[string]any{}
	databaseError := ""
	st, err := s.repo.DatabaseStats(ctx)
	if err != nil {
		databaseError = "读取数据库状态失败"
		plog.From(ctx).Warn("读取数据库统计失败", zap.Error(err))
	} else {
		db = &DatabaseStats{
			SizeBytes:      st.SizeBytes,
			Connections:    st.Connections,
			MaxConnections: st.MaxConnections,
		}
		dbMetrics["size_bytes"] = st.SizeBytes
		dbMetrics["connections"] = st.Connections
		dbMetrics["max_connections"] = st.MaxConnections
	}

	components := s.buildComponents(ctx, now, db, dbMetrics, backup)

	state := StateOK
	for _, c := range components {
		if c.State == StateWarn || c.State == StateDown {
			state = "degraded"
			break
		}
	}

	return SystemStatus{
		Backup:        backup,
		Database:      db,
		DatabaseError: databaseError,
		State:         state,
		Components:    components,
	}, nil
}

// buildComponents 按固定顺序探测 8 个组件。
func (s *Service) buildComponents(ctx context.Context, now time.Time, db *DatabaseStats, dbMetrics map[string]any, backup BackupStatus) []Component {
	out := make([]Component, 0, 8)

	start := time.Now()
	pingErr := s.repo.Ping(ctx)
	out = append(out, postgresComponent(pingErr, time.Since(start), db, dbMetrics))

	vk := Component{Key: "valkey", State: StateUnknown, Metrics: map[string]any{}}
	if s.deps.Redis != nil {
		start = time.Now()
		if err := s.deps.Redis.Ping(ctx); err != nil {
			vk.State, vk.Message = StateDown, "Redis 不可达"
		} else {
			vk.State, vk.LatencyMS = StateOK, millis(time.Since(start))
		}
	}
	out = append(out, vk)

	out = append(out, s.nodeFabricComponent(ctx, now))
	out = append(out, s.paymentCallbacksComponent(ctx, now))

	mail, telegram := s.mailTelegramComponents(ctx)
	out = append(out, mail, telegram)

	sse := Component{
		Key:     "sse",
		State:   StateUnknown,
		Metrics: map[string]any{},
		Message: "实时推送未启用",
	}
	if s.streams != nil {
		sse = Component{
			Key:     "sse",
			State:   StateOK,
			Metrics: map[string]any{"connections": s.streams.Count()},
		}
	}
	out = append(out, sse)

	out = append(out, backupComponent(backup))

	return out
}

// postgresComponent 连不上为 down；连得上但统计没读到为 warn、metrics 空；都好为 ok、三项齐全。
func postgresComponent(pingErr error, latency time.Duration, db *DatabaseStats, dbMetrics map[string]any) Component {
	pg := Component{Key: "postgres", State: StateOK, Metrics: map[string]any{}}
	if pingErr != nil {
		pg.State, pg.Message = StateDown, "数据库不可达"
		return pg
	}
	pg.LatencyMS = millis(latency)
	if db == nil {
		pg.State, pg.Message = StateWarn, "数据库可以连通，但体积与连接数统计读取失败"
		return pg
	}
	pg.Metrics["size_bytes"] = dbMetrics["size_bytes"]
	pg.Metrics["connections"] = dbMetrics["connections"]
	pg.Metrics["max_connections"] = dbMetrics["max_connections"]
	return pg
}

// nodeFabricComponent 有节点不在线或配置落后为 warn；读不到为 unknown。
func (s *Service) nodeFabricComponent(ctx context.Context, now time.Time) Component {
	c := Component{Key: "node_fabric", State: StateUnknown, Metrics: map[string]any{}}
	nodes, err := s.deps.Nodes.ReportNodeCounts(ctx, now)
	if err != nil {
		return c
	}
	lagging, err := s.deps.Nodes.ConfigLaggingCount(ctx)
	if err != nil {
		return c
	}
	c.Metrics = map[string]any{
		"total":          nodes.Total,
		"online":         nodes.Online,
		"config_lagging": lagging,
	}
	c.State = StateOK
	if nodes.Online < nodes.Total || lagging > 0 {
		c.State = StateWarn
	}
	return c
}

// paymentCallbacksComponent 有收到一分钟还没处理的回调为 warn。
func (s *Service) paymentCallbacksComponent(ctx context.Context, now time.Time) Component {
	c := Component{Key: "payment_callbacks", State: StateUnknown, Metrics: map[string]any{}}
	pending, err := s.deps.Payments.StuckCallbackCount(ctx, now.Add(-time.Minute))
	if err != nil {
		return c
	}
	c.Metrics = map[string]any{"pending": pending}
	c.State = StateOK
	if pending > 0 {
		c.State = StateWarn
	}
	return c
}

// mailTelegramComponents 一次读两个渠道的投递计数。
func (s *Service) mailTelegramComponents(ctx context.Context) (Component, Component) {
	mail := Component{Key: "mail", State: StateUnknown, Metrics: map[string]any{}}
	telegram := Component{Key: "telegram", State: StateUnknown, Metrics: map[string]any{}}
	channels, err := s.deps.Notices.DeliveryCountsByChannel(ctx)
	if err != nil {
		return mail, telegram
	}
	mail = deliveryChannelComponent("mail", "email", channels)
	telegram = deliveryChannelComponent("telegram", "telegram", channels)
	return mail, telegram
}

// deliveryChannelComponent 有正在重试的投递为 warn；failed_total 是累计值，不参与判定；渠道从没投递过为 ok、计数全 0。
func deliveryChannelComponent(key, channel string, channels map[string]notifysvc.ChannelDeliveryCounts) Component {
	c := Component{
		Key:   key,
		State: StateOK,
		Metrics: map[string]any{
			"queued":       int64(0),
			"retrying":     int64(0),
			"failed_total": int64(0),
		},
	}
	d, ok := channels[channel]
	if !ok {
		return c
	}
	c.Metrics = map[string]any{
		"queued":       d.Queued,
		"retrying":     d.Retrying,
		"failed_total": d.Failed,
	}
	if d.Retrying > 0 {
		c.State = StateWarn
	}
	return c
}

// millis 把耗时换成毫秒（保留微秒精度）。
func millis(d time.Duration) *float64 {
	v := float64(d.Microseconds()) / 1000
	return &v
}

// probeBackup 直接看备份目录文件系统。
func (s *Service) probeBackup(now time.Time) BackupStatus {
	dir := s.opt.BackupDir
	st := BackupStatus{Dir: dir}

	if dir == "" {
		st.Readable = false
		st.Message = "没有配置备份目录（backup.dir）。"
		return st
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		st.Readable = false
		st.Message = "面板读不到备份目录（" + dir + "）。可能是权限或 systemd 沙箱限制，不代表备份没在跑；用 systemctl status aegis-backup.service 直接确认。"
		return st
	}
	st.Readable = true

	checksums := map[string]bool{}
	seals := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasSuffix(name, ".sha256") {
			checksums[strings.TrimSuffix(name, ".sha256")] = true
		}
		if strings.HasSuffix(name, ".seal") {
			seals[strings.TrimSuffix(name, ".seal")] = true
		}
	}

	files := []BackupFile{}
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
		files = append(files, BackupFile{
			Name:        e.Name(),
			Size:        info.Size(),
			ModTime:     info.ModTime(),
			HasChecksum: checksums[e.Name()],
			HasSeal:     seals[e.Name()],
		})
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].ModTime.After(files[j].ModTime)
	})

	st.Count = len(files)
	st.TotalBytes = total

	var missingChecksum, missingSeal int
	if len(files) > 0 {
		for _, f := range files {
			if !f.HasChecksum {
				missingChecksum++
			}
			if !f.HasSeal {
				missingSeal++
			}
		}
		st.MissingChecksum = &missingChecksum
		st.MissingSeal = &missingSeal
	}

	var latest *BackupFile
	for i := range files {
		if files[i].HasChecksum && files[i].HasSeal {
			latest = &files[i]
			break
		}
	}
	if latest != nil {
		st.Latest = latest
		ageH := max(0, int(now.Sub(latest.ModTime).Hours())) // 文件时间比面板时钟还新时按 0 算
		st.LatestAgeHours = &ageH
		st.Stale = now.Sub(latest.ModTime) > backupStaleAfter
	} else {
		st.Stale = true
	}

	st.Recent = files[:min(backupRecent, len(files))]

	offsite := false
	if _, err := os.Stat(filepath.Join(dir, "backup-webdav.json")); err == nil {
		offsite = true
	} else {
		for _, p := range offsiteConfigPaths {
			if _, err := os.Stat(p); err == nil {
				offsite = true
				break
			}
		}
	}
	st.OffsiteConfigured = offsite

	return st
}

// backupComponent 由备份目录探测结果派生 backup 组件。
func backupComponent(backup BackupStatus) Component {
	bk := Component{Key: "backup", State: StateUnknown, Metrics: map[string]any{}}
	if !backup.Readable {
		if backup.Message != "" {
			bk.Message = backup.Message
		}
		return bk
	}

	var age any
	if backup.LatestAgeHours != nil {
		age = *backup.LatestAgeHours
	}
	missing := 0
	if backup.MissingChecksum != nil {
		missing = *backup.MissingChecksum
	}
	unsealed := 0
	if backup.MissingSeal != nil {
		unsealed = *backup.MissingSeal
	}
	bk.Metrics = map[string]any{
		"latest_age_hours":   age,
		"stale":              backup.Stale,
		"offsite_configured": backup.OffsiteConfigured,
	}
	bk.State = StateOK
	if backup.Stale || !backup.OffsiteConfigured || missing > 0 || unsealed > 0 {
		bk.State = StateWarn
	}
	return bk
}
