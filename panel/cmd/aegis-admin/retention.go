package main

import (
	"log/slog"
	"time"
)

const (
	// retentionTick 是保留期循环的节拍：按天汇总要在日界后及时做。
	retentionTick = 10 * time.Minute
	// retentionPurgeEvery 是各项保留期清理的间隔（挂在 retentionTick 的节拍上）。
	retentionPurgeEvery = time.Hour
)

// retentionPurge 是一轮保留期清理的结果：每项删了多少行、有没有出错。
type retentionPurge struct {
	alive, metrics, rollups, activity, reports, fetchLogs                   int64
	aliveErr, metricsErr, rollupsErr, activityErr, reportsErr, fetchLogsErr error
}

// ok 报告这一轮是不是全部成功。
func (p *retentionPurge) ok() bool {
	return p.aliveErr == nil && p.metricsErr == nil && p.rollupsErr == nil &&
		p.activityErr == nil && p.reportsErr == nil && p.fetchLogsErr == nil
}

// report 把出错的项记成错误日志，删过行的记成一条信息日志。
func (p *retentionPurge) report(log *slog.Logger) {
	for _, e := range []struct {
		err  error
		msg  string
		rows int64
	}{
		{p.aliveErr, "在线记录清理失败", p.alive},
		{p.metricsErr, "探针点清理失败", p.metrics},
		{p.rollupsErr, "流量小时汇总清理失败", p.rollups},
		{p.activityErr, "行为趋势按天汇总清理失败", p.activity},
		{p.reportsErr, "流量上报留档清理失败", p.reports},
		{p.fetchLogsErr, "订阅拉取日志清理失败", p.fetchLogs},
	} {
		if e.err != nil {
			log.Error(e.msg, "error", e.err.Error(), "deleted", e.rows)
		}
	}
	if p.alive > 0 || p.metrics > 0 || p.rollups > 0 || p.activity > 0 || p.reports > 0 || p.fetchLogs > 0 {
		log.Info("保留期清理完成", "alive_ips", p.alive, "node_metrics", p.metrics, "traffic_rollups", p.rollups,
			"activity_daily", p.activity, "traffic_reports", p.reports, "fetch_logs", p.fetchLogs)
	}
}
