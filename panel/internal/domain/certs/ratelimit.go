package certs

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// 本地限额对账（设计稿 §1.3）：下单前按 certificate_versions 数过去 7 天签了多少，预计会超 Let's Encrypt
// 的限额就不去碰 CA，直接退避到最早那张满 7 天的时候。宁可多数（跨证书、跨 CA 一起数）也不少数：
// 撞上 CA 的限额要等一整周，本地多等几小时代价小得多。
const (
	limitWindow        = 7 * 24 * time.Hour
	perDomainWeekLimit = 50 // 每注册域每 7 天新证书
	exactSetWeekLimit  = 5  // 完全相同的标识集合每 7 天
)

// recentIssuance 是过去 7 天签出的一张证书。
type recentIssuance struct {
	identifiers []string
	isRenewal   bool
	createdAt   time.Time
}

// loadRecentIssuances 读过去 7 天本租户签出的全部版本（量很小：一周最多几百张），连同库时钟。
func loadRecentIssuances(ctx context.Context, tx pgx.Tx, tenantID string) ([]recentIssuance, time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT now()`).Scan(&now); err != nil {
		return nil, now, err
	}
	rows, err := tx.Query(ctx, `
		SELECT identifiers, is_renewal, created_at FROM certificate_versions
		 WHERE tenant_id = $1 AND created_at > $2`, tenantID, now.Add(-limitWindow))
	if err != nil {
		return nil, now, err
	}
	defer rows.Close()
	var out []recentIssuance
	for rows.Next() {
		var r recentIssuance
		if err := rows.Scan(&r.identifiers, &r.isRenewal, &r.createdAt); err != nil {
			return nil, now, err
		}
		out = append(out, r)
	}
	return out, now, rows.Err()
}

// limitDecision 是对账结论。
type limitDecision struct {
	blocked bool
	reason  string
	retryAt time.Time
}

// checkLocalLimits 判断这一单会不会超限：
//   - 带 ARI replaces 的续期：Let's Encrypt 免一切限额，不拦；
//   - 完全相同的标识集合 7 天内已有 5 张：拦；
//   - 不是续期（首次或标识变了）时，任一注册域 7 天内的新证书已有 50 张：拦。
//
// retryAt 是让计数降到限额以下的最早时间（最早那张满 7 天）。
func checkLocalLimits(recent []recentIssuance, ids []string, renewal, ariReplaces bool, now time.Time) limitDecision {
	if ariReplaces {
		return limitDecision{}
	}
	var same []time.Time
	for _, r := range recent {
		if slices.Equal(r.identifiers, ids) {
			same = append(same, r.createdAt)
		}
	}
	if len(same) >= exactSetWeekLimit {
		return limitDecision{blocked: true, retryAt: unblockAt(same, exactSetWeekLimit),
			reason: fmt.Sprintf("同一组域名 7 天内已签 %d 张，达到 Let's Encrypt 每周 %d 张的上限", len(same), exactSetWeekLimit)}
	}
	if renewal {
		return limitDecision{}
	}
	domains := map[string]bool{}
	for _, id := range ids {
		domains[registeredDomain(id)] = true
	}
	for rd := range domains {
		var hits []time.Time
		for _, r := range recent {
			if r.isRenewal {
				continue
			}
			for _, id := range r.identifiers {
				if registeredDomain(id) == rd {
					hits = append(hits, r.createdAt)
					break
				}
			}
		}
		if len(hits) >= perDomainWeekLimit {
			return limitDecision{blocked: true, retryAt: unblockAt(hits, perDomainWeekLimit),
				reason: fmt.Sprintf("%s 7 天内已签 %d 张新证书，达到 Let's Encrypt 每注册域每周 %d 张的上限", rd, len(hits), perDomainWeekLimit)}
		}
	}
	return limitDecision{}
}

// unblockAt 是计数降到 limit-1 的时间：按时间排序后第 len-limit+1 早的那张满 7 天时。
func unblockAt(times []time.Time, limit int) time.Time {
	slices.SortFunc(times, func(a, b time.Time) int { return a.Compare(b) })
	return times[len(times)-limit].Add(limitWindow)
}
