package service

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/aegispanel/aegis/internal/module/certs/model"
	"github.com/google/uuid"
)

// 本地限额对账：下单前按只追加的签发流水（certificate_issuances）数过去 7 天签了多少，预计会超
// Let's Encrypt 的限额就不去碰 CA，直接退避到最早那张满 7 天的时候。流水不随删证书删除，租约丢失、
// 结果被丢弃的那张也记着。
//   - 「同一组域名每周 5 张」是 CA 自己的账：只数同一个 CA 签的（staging、测试 CA、ZeroSSL 不占
//     Let's Encrypt 正式环境的额度）；
//   - 「每注册域每周 50 张」宁可多数：跨 CA 一起数。撞上 CA 的限额要等一整周，本地多等几小时代价小得多。
const (
	limitWindow        = 7 * 24 * time.Hour
	perDomainWeekLimit = 50 // 每注册域每 7 天新证书
	exactSetWeekLimit  = 5  // 完全相同的标识集合每 7 天
)

// recentIssuance 是过去 7 天签出的一张证书。
type recentIssuance struct {
	ca          string
	identifiers []string
	isRenewal   bool
	createdAt   time.Time
}

// limitDecision 是对账结论。
type limitDecision struct {
	blocked bool
	reason  string
	retryAt time.Time
}

// checkLocalLimits 判断这一单会不会超限：
//   - 带 ARI replaces 的续期：Let's Encrypt 免一切限额，不拦；
//   - 同一个 CA 7 天内已为完全相同的标识集合签过 5 张：拦；
//   - 不是续期（首次或标识变了）时，任一注册域 7 天内的新证书已有 50 张：拦。
//
// retryAt 是让计数降到限额以下的最早时间（最早那张满 7 天）。
func checkLocalLimits(recent []recentIssuance, ids []string, ca string, renewal, ariReplaces bool, now time.Time) limitDecision {
	if ariReplaces {
		return limitDecision{}
	}
	var same []time.Time
	for _, r := range recent {
		if r.ca == ca && slices.Equal(r.identifiers, ids) {
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

// loadRecentIssuances 读过去 7 天的签发流水，连同库时钟（限额对账用）。
func (s *Service) loadRecentIssuances(ctx context.Context) ([]recentIssuance, time.Time, error) {
	now, err := s.store.DBNow(ctx)
	if err != nil {
		return nil, now, err
	}
	rows, err := s.store.IssuancesSince(ctx, now.Add(-limitWindow))
	if err != nil {
		return nil, now, err
	}
	out := make([]recentIssuance, 0, len(rows))
	for _, r := range rows {
		ids, err := decodeIDs(r.Identifiers)
		if err != nil {
			return nil, now, err
		}
		out = append(out, recentIssuance{ca: r.CA, identifiers: ids, isRenewal: r.IsRenewal, createdAt: r.CreatedAt})
	}
	return out, now, nil
}

// recordIssuance 在 CA 签出时写签发流水；用不随任务取消的 context，签发协程可能比任务活得久。
func (s *Service) recordIssuance(ctx context.Context, certID uuid.UUID, target caTarget, ids []string, renewal, ariReplaces bool, iss *issued) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	return s.store.InsertIssuance(ctx, &model.CertificateIssuance{
		ID: uuid.Must(uuid.NewV7()), CertificateID: certID, CA: target.CA, Identifiers: encodeIDs(ids),
		IsRenewal: renewal, ARIReplaces: ariReplaces, Serial: serialHex(iss.leaf),
	})
}
