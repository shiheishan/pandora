package service

// 节点在线升级：前置检查 → 事务建任务 → 提交后经会话下发 Command.upgrade → 节点回报与 Hello 对账 → 超时巡检。
// 信任根在节点，面板只搬运发布清单与签名（目标版本见 pdnd_manifest.go）。

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	auditsvc "github.com/aegispanel/aegis/internal/module/audit/service"
	"github.com/aegispanel/aegis/internal/module/node/model"
	"github.com/aegispanel/aegis/internal/module/node/repo"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/aegispanel/aegis/internal/pkg/realtime"
	"github.com/aegispanel/aegis/internal/pkg/tx"
)

const upgradeDetailMaxBytes = 1024
const upgradeWatchBatch = 500

// upgradeFacts 是判断一台服务器能不能升级要的事实。
type upgradeFacts struct {
	Status       string
	BinarySHA256 *string
	Online       bool
	Session      SessionUpgradeInfo
	OpenJob      bool
}

// upgradeBlock 返回这台服务器要用的产物与不能升级的原因码（空串 = 能升级）。
func upgradeBlock(t PdndTarget, f upgradeFacts) (PdndTargetArtifact, string) {
	if f.OpenJob {
		return PdndTargetArtifact{}, UpgradeBlockInProgress
	}
	if !t.Available {
		return PdndTargetArtifact{}, t.Reason
	}
	if f.Status != ServerReady {
		return PdndTargetArtifact{}, UpgradeBlockNotReady
	}
	if !f.Online {
		return PdndTargetArtifact{}, UpgradeBlockNotConnected
	}
	if !f.Session.UpgradeV1 {
		return PdndTargetArtifact{}, UpgradeBlockNoSupport
	}
	art, ok := t.Artifacts[pdndArchKey(f.Session.Arch)]
	if !ok {
		return PdndTargetArtifact{}, UpgradeBlockArchUnsupported
	}
	if !art.Available {
		reason := art.Reason
		if reason == "" {
			reason = UpgradeBlockBinaryMismatch
		}
		return PdndTargetArtifact{}, reason
	}
	if f.BinarySHA256 != nil && *f.BinarySHA256 == art.SHA256 {
		return PdndTargetArtifact{}, UpgradeBlockUpToDate
	}
	if f.Session.BuildSerial >= t.Serial {
		return PdndTargetArtifact{}, UpgradeBlockSerialNotNewer
	}
	return art, ""
}

func upgradeJobView(m model.ServerUpgrade) ServerUpgradeJob {
	updated := m.CreatedAt
	for _, t := range []*time.Time{m.SentAt, m.StagedAt, m.FinishedAt} {
		if t != nil && t.After(updated) {
			updated = *t
		}
	}
	return ServerUpgradeJob{
		ID:            m.ID,
		Status:        m.Status,
		TargetVersion: m.TargetVersion,
		ReasonCode:    m.ReasonCode,
		UpdatedAt:     updated,
	}
}

func upgradeOpen(status string) bool {
	return status == UpgradeSent || status == UpgradeStaged
}

func pdndTargetReasonText(code string) string {
	switch code {
	case UpgradeBlockManifestMissing:
		return "面板上没有节点端发布清单"
	case UpgradeBlockSignatureMissing:
		return "面板上没有有效的发布清单签名"
	case UpgradeBlockManifestInvalid:
		return "发布清单格式不对"
	case UpgradeBlockBinaryMismatch:
		return "面板托管的节点端程序与发布清单不符"
	default:
		return code
	}
}

func (s *Service) upgradeFactsFor(m model.Server, latest *model.ServerUpgrade) upgradeFacts {
	f := upgradeFacts{
		Status:       m.Status,
		BinarySHA256: m.BinarySHA256,
	}
	if latest != nil && upgradeOpen(latest.Status) {
		f.OpenJob = true
	}
	if s.sessions != nil {
		info, ok := s.sessions.UpgradeInfo(m.ID)
		f.Online = ok
		f.Session = info
	}
	return f
}

// ServerUpgradeViews 给后台列表与详情：一次读清单、一条 SQL 读最近任务、会话事实在内存拼。
func (s *Service) ServerUpgradeViews(ctx context.Context, servers []model.Server) (map[uuid.UUID]ServerUpgradeOption, map[uuid.UUID]ServerUpgradeJob, error) {
	opts := make(map[uuid.UUID]ServerUpgradeOption)
	jobs := make(map[uuid.UUID]ServerUpgradeJob)
	if len(servers) == 0 {
		return opts, jobs, nil
	}
	ids := make([]uuid.UUID, len(servers))
	for i, m := range servers {
		ids[i] = m.ID
	}
	t := s.PdndTarget(ctx)
	latest, err := s.repo.LatestServerUpgrades(ctx, ids)
	if err != nil {
		return nil, nil, err
	}
	for _, m := range servers {
		var latestPtr *model.ServerUpgrade
		if u, ok := latest[m.ID]; ok {
			latestPtr = &u
			jobs[m.ID] = upgradeJobView(u)
		}
		_, reason := upgradeBlock(t, s.upgradeFactsFor(m, latestPtr))
		opts[m.ID] = ServerUpgradeOption{
			Available:     reason == "",
			TargetVersion: t.Version,
			Reason:        reason,
		}
	}
	return opts, jobs, nil
}

// RequestUpgrades 批量发起在线升级：逐台前置与结果，过了前置的在一个事务里建任务，提交后经会话下发。
func (s *Service) RequestUpgrades(ctx context.Context, serverIDs []uuid.UUID) (UpgradeBatch, error) {
	ids := uniqueUUIDsInOrder(serverIDs)
	if len(ids) == 0 || len(ids) > UpgradeBatchMax {
		return UpgradeBatch{}, errs.Invalid(map[string]string{"server_ids": "一次选 1 到 50 台服务器"})
	}
	if s.pdnd.origin == "" {
		return UpgradeBatch{}, errs.Wrap(errs.Unavailable, errors.New("节点端安装分发没有装配（UsePdndDist）"))
	}
	t := s.PdndTarget(ctx)
	if !t.Available {
		return UpgradeBatch{}, errs.Newf(errs.Conflict, "现在不能在线升级："+pdndTargetReasonText(t.Reason))
	}
	now := s.now()
	var createdBy *uuid.UUID
	if p, ok := authn.From(ctx); ok && p.UserID != uuid.Nil {
		u := p.UserID
		createdBy = &u
	}

	results := make([]UpgradeResult, len(ids))
	for i, id := range ids {
		results[i].ServerID = id
	}

	var batch UpgradeBatch
	err := s.tx.Do(ctx, func(ctx context.Context) error {
		rows, err := s.repo.ServersByIDs(ctx, ids)
		if err != nil {
			return err
		}
		byID := make(map[uuid.UUID]model.Server, len(rows))
		for _, m := range rows {
			byID[m.ID] = m
		}
		latest, err := s.repo.LatestServerUpgrades(ctx, ids)
		if err != nil {
			return err
		}

		type candidate struct {
			insert repo.ServerUpgradeInsert
			file   string
			idx    int
		}
		var candidates []candidate

		for i, id := range ids {
			m, ok := byID[id]
			if !ok {
				results[i].Reason = UpgradeBlockNotFound
				continue
			}
			var latestPtr *model.ServerUpgrade
			if u, ok := latest[id]; ok {
				latestPtr = &u
			}
			art, reason := upgradeBlock(t, s.upgradeFactsFor(m, latestPtr))
			if reason != "" {
				results[i].Reason = reason
				continue
			}
			candidates = append(candidates, candidate{
				insert: repo.ServerUpgradeInsert{
					ServerID:      id,
					TargetVersion: t.Version,
					TargetSerial:  int64(t.Serial), //nolint:gosec // 读清单时已限定 serial ≤ MaxInt64（pdnd_manifest.go）
					TargetSHA256:  art.SHA256,
					FromVersion:   m.AgentVersion,
					FromSHA256:    m.BinarySHA256,
					CreatedBy:     createdBy,
					CreatedAt:     now,
					DeadlineAt:    now.Add(UpgradeDeadline),
				},
				file: art.File,
				idx:  i,
			})
		}

		inserts := make([]repo.ServerUpgradeInsert, len(candidates))
		for i, c := range candidates {
			inserts[i] = c.insert
		}
		created, err := s.repo.InsertServerUpgrades(ctx, inserts)
		if err != nil {
			return err
		}

		createdServerIDs := make([]uuid.UUID, 0, len(created))
		createdFiles := make(map[uuid.UUID]string, len(created))
		for _, c := range candidates {
			_, ok := created[c.insert.ServerID]
			if !ok {
				results[c.idx].Reason = UpgradeBlockInProgress
				continue
			}
			results[c.idx].OK = true
			createdServerIDs = append(createdServerIDs, c.insert.ServerID)
			createdFiles[c.insert.ServerID] = c.file
		}

		idsStr := make([]string, len(createdServerIDs))
		for i, id := range createdServerIDs {
			idsStr[i] = id.String()
		}
		if err := s.audit.Write(ctx, auditsvc.Event{
			Action:       "server.upgrade_requested",
			ResourceType: "server_set",
			Data: map[string]any{
				"server_ids":     idsStr,
				"target_version": t.Version,
				"target_serial":  t.Serial,
				"requested":      len(ids),
				"rejected":       len(ids) - len(created),
			},
		}); err != nil {
			return err
		}

		batch = UpgradeBatch{TargetVersion: t.Version, Results: results}
		if len(created) == 0 {
			return nil
		}

		dispatchIDs := make([]uuid.UUID, 0, len(created))
		for _, id := range ids {
			if _, ok := created[id]; ok {
				dispatchIDs = append(dispatchIDs, id)
			}
		}

		tx.AfterCommit(ctx, "node:upgrade_dispatch", func(ctx context.Context) {
			log := plog.From(ctx)
			for _, serverID := range dispatchIDs {
				jobID := created[serverID]
				file := createdFiles[serverID]
				cmd := UpgradeCommand{
					JobID:            jobID,
					Manifest:         t.Manifest,
					Signature:        t.Signature,
					BinaryURL:        s.pdnd.origin + "/pdnd/bin/" + file,
					ConfirmWithinSec: UpgradeConfirmWithinSec,
				}
				sent := s.sessions != nil && s.sessions.SendUpgrade(serverID, cmd)
				if sent {
					if err := s.repo.MarkServerUpgradeSent(ctx, jobID, s.now()); err != nil {
						log.Warn("标记升级任务已下发失败", zap.String("server_id", serverID.String()), zap.String("job_id", jobID.String()), zap.Error(err))
					}
				} else {
					reason := UpgradeBlockNotConnected
					if _, err := s.repo.FinishServerUpgrade(ctx, jobID, serverID, []string{UpgradeSent}, UpgradeFailed, &reason, nil, s.now()); err != nil {
						log.Warn("下发失败时结束升级任务失败", zap.String("server_id", serverID.String()), zap.String("job_id", jobID.String()), zap.Error(err))
					}
					for i := range batch.Results {
						if batch.Results[i].ServerID == serverID {
							batch.Results[i].OK = false
							batch.Results[i].Reason = UpgradeBlockNotConnected
							break
						}
					}
				}
				s.serverChanged(ctx, realtime.OpUpdate, serverID)
			}
		})
		return nil
	})
	if err != nil {
		return UpgradeBatch{}, err
	}
	return batch, nil
}

func uniqueUUIDsInOrder(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]bool, len(ids))
	out := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// RecordUpgradeReport 按节点回报对账升级任务（SessionBackend）。
// Staged：sent → staged；Failed、RolledBack：未结束、succeeded、timed_out → failed / rolled_back
// （新进程连上判了 succeeded 之后，助手仍可能确认超时去回退，回退失败报 FAILED；超时是面板猜测；都以节点回报为准）。
// 影响 0 行视为已处理；改到了推 nodes.changed。
func (s *Service) RecordUpgradeReport(ctx context.Context, serverID uuid.UUID, r UpgradeReport, at time.Time) error {
	if r.JobID == uuid.Nil {
		return nil
	}
	reason := "unknown"
	if c := cleanText(r.ReasonCode, maxFailReasonChars); c != nil {
		reason = *c
	}
	detail := truncateBytes(r.Detail, upgradeDetailMaxBytes)

	var changed bool
	var err error
	switch r.Phase {
	case UpgradePhaseStaged:
		changed, err = s.repo.StageServerUpgrade(ctx, r.JobID, serverID, at)
	case UpgradePhaseFailed:
		changed, err = s.repo.FinishServerUpgrade(ctx, r.JobID, serverID,
			[]string{UpgradeSent, UpgradeStaged, UpgradeSucceeded, UpgradeTimedOut}, UpgradeFailed, &reason, detail, at)
	case UpgradePhaseRolledBack:
		// 新进程已连上并判 succeeded 后，仍可能因未写 healthy 被助手装回旧版；以节点回报为准。
		changed, err = s.repo.FinishServerUpgrade(ctx, r.JobID, serverID,
			[]string{UpgradeSent, UpgradeStaged, UpgradeSucceeded, UpgradeTimedOut}, UpgradeRolledBack, &reason, detail, at)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if changed {
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
	}
	return nil
}

// succeedUpgradeBySHA：新 Hello 自报的摘要等于未结束任务（或这台最近一次已超时的任务）的目标 = 升级成功。
func (s *Service) succeedUpgradeBySHA(ctx context.Context, serverID uuid.UUID, h Hello, at time.Time) error {
	sha := agentInfo(h).BinarySHA256
	if sha == nil {
		return nil
	}
	ok, err := s.repo.SucceedServerUpgradeBySHA(ctx, serverID, *sha, at)
	if err != nil {
		return err
	}
	if ok {
		s.serverChanged(ctx, realtime.OpUpdate, serverID)
	}
	return nil
}

// WatchUpgrades 超时巡检（worker 任务 node:upgrade_watch 每分钟调）。
func (s *Service) WatchUpgrades(ctx context.Context) (int, error) {
	ids, err := s.repo.TimeoutServerUpgrades(ctx, s.now(), upgradeWatchBatch)
	if err != nil {
		return 0, err
	}
	seen := make(map[uuid.UUID]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		s.serverChanged(ctx, realtime.OpUpdate, id)
	}
	return len(ids), nil
}
