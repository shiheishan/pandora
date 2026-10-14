package main

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"
)

// newID 生成 uuidv7（与库里 DEFAULT uuidv7() 同类，按时间有序）。
func newID() string {
	return uuid.Must(uuid.NewV7()).String()
}

func validateGenInput(in GenInput) error {
	if in.Users < 1 {
		return fmt.Errorf("用户数至少为 1")
	}
	if in.ExpiredCount < 0 || in.ExpiredCount >= in.Users {
		return fmt.Errorf("已过期用户数须在 [0, 用户数) 内")
	}
	if in.ExpireCount < 0 || in.ExpireCount > in.Users-in.ExpiredCount {
		return fmt.Errorf("突发到期份数不能超过生效中用户数")
	}
	if (in.ExpireAt != nil) != (in.ExpireCount > 0) {
		return fmt.Errorf("ExpireAt 与 ExpireCount 须同时有或同时无")
	}
	if in.ExpireAt != nil {
		min := in.Now.Add(10 * time.Minute)
		if !in.ExpireAt.After(min) {
			return fmt.Errorf("ExpireAt 须晚于当前时间 10 分钟以上")
		}
	}
	if in.HistoryDays < 0 || in.HistoryDays > 400 {
		return fmt.Errorf("HistoryDays 须在 0–400 内")
	}
	if in.Loc == nil {
		return fmt.Errorf("Loc 不能为空")
	}
	if in.GroupID == "" {
		return fmt.Errorf("GroupID 不能为空")
	}
	if in.OfflineProviderID == "" {
		return fmt.Errorf("OfflineProviderID 不能为空")
	}
	if len(in.Plans) < 1 {
		return fmt.Errorf("至少需要一个套餐")
	}
	for _, p := range in.Plans {
		if p.Interval != "month" || p.IntervalCount != 1 {
			return fmt.Errorf("套餐仅支持月付、周期 1 个月")
		}
		for _, q := range p.Quotas {
			if q.Period != "cycle" && q.Period != "total" {
				return fmt.Errorf("配额周期只能是 cycle 或 total")
			}
		}
	}
	return nil
}

func jsonTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func planMaxDaily(plan PlanSpec) int64 {
	maxDaily := int64(2 << 30)
	for _, q := range plan.Quotas {
		if q.Metric != "traffic.bytes" || q.Limit == nil {
			continue
		}
		lim := *q.Limit / 2 / 31
		if lim < maxDaily {
			maxDaily = lim
		}
	}
	if maxDaily < 1 {
		maxDaily = 1
	}
	return maxDaily
}

type userTimeline struct {
	T0         time.Time
	ends       []time.Time
	k          int
	expired    bool
	override   bool
	cycleStart time.Time
	cycleEnd   time.Time
	periodEnd  time.Time
}

func buildTimeline(i, users, expiredCount, expireCount int, now time.Time, expireAt *time.Time, r *rand.Rand) (userTimeline, error) {
	expired := i >= users-expiredCount
	override := i < expireCount
	var k int
	var T0 time.Time
	if expired {
		k = r.IntN(2)
		e := 4*24*time.Hour + time.Duration(r.Int64N(int64(21*24*time.Hour)))
		T0 = now.Add(-e).AddDate(0, -(k + 1), 0).Truncate(time.Second)
	} else {
		k = r.IntN(3)
		if override {
			k = 0
		}
		d := 4*24*time.Hour + time.Duration(r.Int64N(int64(21*24*time.Hour)))
		T0 = now.AddDate(0, -k, 0).Add(-d).Truncate(time.Second)
	}
	ends := make([]time.Time, k+1)
	ends[0] = T0.AddDate(0, 1, 0)
	for j := 1; j <= k; j++ {
		ends[j] = ends[j-1].AddDate(0, 1, 0)
	}
	cycleStart := T0
	if k > 0 {
		cycleStart = ends[k-1]
	}
	cycleEnd := ends[k]
	periodEnd := cycleEnd
	if override && expireAt != nil {
		periodEnd = *expireAt
		cycleEnd = *expireAt
	}
	tl := userTimeline{
		T0: T0, ends: ends, k: k, expired: expired, override: override,
		cycleStart: cycleStart, cycleEnd: cycleEnd, periodEnd: periodEnd,
	}
	if expired {
		if ends[k].After(now.Add(-time.Hour)) {
			return tl, fmt.Errorf("内部错误：用户 %d 的时间线不自洽", i+1)
		}
	} else {
		if cycleStart.After(now.Add(-3*time.Hour)) || !ends[k].After(now.Add(time.Hour)) {
			return tl, fmt.Errorf("内部错误：用户 %d 的时间线不自洽", i+1)
		}
	}
	return tl, nil
}

func dayOf(t time.Time, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func dayStart(day time.Time, loc *time.Location) time.Time {
	y, m, d := day.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, loc)
}

func periodIndex(t time.Time, T0 time.Time, ends []time.Time, k int) int {
	if t.Before(T0) {
		return k
	}
	for j := 0; j <= k; j++ {
		start := T0
		if j > 0 {
			start = ends[j-1]
		}
		end := ends[j]
		if !t.Before(start) && t.Before(end) {
			return j
		}
	}
	return k
}

func strPtr(s string) *string { return &s }

// Generate 按 GenInput 生成写库所需的全部行（不写库）。
func Generate(in GenInput, issue CredentialIssuer) (*Seed, error) {
	if err := validateGenInput(in); err != nil {
		return nil, err
	}
	now := in.Now.UTC()
	out := &Seed{}
	orderSeq := 1

	for i := 0; i < in.Users; i++ {
		r := rand.New(rand.NewPCG(in.Seed, uint64(i)))
		n := i + 1
		plan := in.Plans[i%len(in.Plans)]
		tl, err := buildTimeline(i, in.Users, in.ExpiredCount, in.ExpireCount, now, in.ExpireAt, r)
		if err != nil {
			return nil, err
		}

		userID := newID()
		createdAt := tl.T0.Add(-time.Duration(1+r.IntN(10)) * 24 * time.Hour).
			Add(-time.Duration(r.Int64N(int64(12 * time.Hour)))).Truncate(time.Second)
		emailVerified := createdAt.Add(10 * time.Minute)

		out.Users = append(out.Users, UserRow{
			ID: userID, Email: userEmail(n), GroupID: in.GroupID,
			EmailVerifiedAt: emailVerified, CreatedAt: createdAt,
		})

		regData, _ := json.Marshal(map[string]any{"email": userEmail(n)})
		out.Audits = append(out.Audits, AuditRow{
			ActorKind: "user", ActorID: strPtr(userID), ActorAudience: strPtr("public"),
			AuthContext: nil, Action: "user.registered", ResourceType: "user", ResourceID: userID,
			Data: string(regData), OccurredAt: createdAt,
		})

		subID := newID()
		status := "active"
		if tl.expired {
			status = "expired"
		}

		k := tl.k
		paid := make([]time.Time, k+1)
		paid[0] = tl.T0
		for j := 1; j <= k; j++ {
			paid[j] = tl.ends[j-1].Add(-48 * time.Hour).Add(-time.Duration(r.Int64N(int64(24 * time.Hour)))).Truncate(time.Second)
		}

		lastEventAt := tl.T0
		orderIDs := make([]string, k+1)

		for j := 0; j <= k; j++ {
			kind := "new"
			if j > 0 {
				kind = "renewal"
			}
			orderID := newID()
			orderIDs[j] = orderID
			paidAt := paid[j].UTC().Truncate(time.Second)
			createdOrder := paidAt.Add(-3 * time.Minute)
			expiresAt := createdOrder.Add(30 * time.Minute)
			orderNo := "AO" + paidAt.Format("20060102") + "-P" + fmt.Sprintf("%07d", orderSeq)
			orderSeq++

			out.Orders = append(out.Orders, OrderRow{
				ID: orderID, OrderNo: orderNo, UserID: userID, Kind: kind,
				Currency: plan.Currency, Amount: plan.Amount, SubscriptionID: subID,
				CreatedAt: createdOrder, ExpiresAt: expiresAt, PaidAt: paidAt,
			})
			out.OrderItems = append(out.OrderItems, OrderItemRow{
				ID: newID(), OrderID: orderID, PriceID: plan.PriceID, PlanID: plan.PlanID,
				PlanVersionID: plan.VersionID, Name: plan.Name, VersionNo: plan.VersionNo,
				Interval: plan.Interval, IntervalCount: plan.IntervalCount,
				EntitlementsJSON: plan.EntitlementsJSON, QuotasJSON: plan.QuotasJSON,
				Amount: plan.Amount, Currency: plan.Currency, CreatedAt: createdOrder,
			})
			out.Reservations = append(out.Reservations, ReservationRow{
				ID: newID(), OrderID: orderID, UserID: userID,
				ExpiresAt: expiresAt, HeldAt: createdOrder, CapturedAt: paidAt,
			})

			ref := "PERFSEED-" + orderNo
			payID := newID()
			provPay := "offline:" + ref
			out.Payments = append(out.Payments, PaymentRow{
				ID: payID, OrderID: orderID, UserID: userID, ProviderPaymentID: provPay,
				Currency: plan.Currency, Amount: plan.Amount, PaidAt: paidAt,
			})
			rawPayload := fmt.Sprintf(`{"source":"admin_offline","actor_id":"","reference":%q,"reason":"perfseed 历史订单"}`, ref)
			out.PaymentEvents = append(out.PaymentEvents, PaymentEventRow{
				ProviderEventID:   "offline:" + orderID + ":" + ref,
				ProviderPaymentID: provPay, RawPayload: rawPayload, ReceivedAt: paidAt,
			})
			out.LedgerTxns = append(out.LedgerTxns, LedgerTxnRow{
				ID: newID(), OrderID: orderID, Currency: plan.Currency, Amount: plan.Amount, OccurredAt: paidAt,
			})

			ocData, _ := json.Marshal(map[string]any{
				"order_no": orderNo, "kind": kind, "amount": plan.Amount, "currency": plan.Currency,
			})
			out.Audits = append(out.Audits, AuditRow{
				ActorKind: "user", ActorID: strPtr(userID), ActorAudience: strPtr("public"),
				AuthContext: strPtr("session"), Action: "order.created", ResourceType: "order", ResourceID: orderID,
				Data: string(ocData), OccurredAt: createdOrder,
			})
			psData, _ := json.Marshal(map[string]any{
				"order_no": orderNo, "kind": kind, "amount": plan.Amount, "fee": "0.00",
				"currency": plan.Currency, "provider": "offline", "method": "offline", "payment_id": payID,
			})
			out.Audits = append(out.Audits, AuditRow{
				ActorKind: "system", ActorID: nil, ActorAudience: nil, AuthContext: nil,
				Action: "payment.succeeded", ResourceType: "order", ResourceID: orderID,
				Data: string(psData), OccurredAt: paidAt,
			})

			if paidAt.After(lastEventAt) {
				lastEventAt = paidAt
			}
		}

		cred, err := issue(subID)
		if err != nil {
			return nil, err
		}
		out.Credentials = append(out.Credentials, CredentialRow{
			ID: newID(), SubscriptionID: subID, UserID: userID,
			TokenHash: cred.Hash, TokenPrefix: cred.Prefix, TokenEncrypted: cred.Sealed, CreatedAt: tl.T0,
		})
		out.CSV = append(out.CSV, CSVRow{UserID: userID, Email: userEmail(n), SubToken: cred.Token})

		actPayload, _ := json.Marshal(map[string]any{
			"plan_version_id": plan.VersionID,
			"period_start":    jsonTime(tl.T0),
			"period_end":      jsonTime(tl.ends[0]),
		})
		out.SubEvents = append(out.SubEvents, SubEventRow{
			ID: newID(), SubscriptionID: subID, EventType: "activated",
			FromStatus: nil, ToStatus: "active", ActorKind: "payment", OrderID: strPtr(orderIDs[0]),
			Payload: string(actPayload), OccurredAt: tl.T0,
		})

		for j := 1; j <= k; j++ {
			renPayload, _ := json.Marshal(map[string]any{
				"source": "order", "restart": false,
				"previous_end": jsonTime(tl.ends[j-1]),
				"period_end":   jsonTime(tl.ends[j]),
			})
			out.SubEvents = append(out.SubEvents, SubEventRow{
				ID: newID(), SubscriptionID: subID, EventType: "renewed",
				FromStatus: strPtr("active"), ToStatus: "active", ActorKind: "payment", OrderID: strPtr(orderIDs[j]),
				Payload: string(renPayload), OccurredAt: paid[j],
			})
			if paid[j].After(lastEventAt) {
				lastEventAt = paid[j]
			}
		}

		if tl.expired {
			expAt := tl.ends[k].Add(time.Minute)
			expPayload, _ := json.Marshal(map[string]any{})
			out.SubEvents = append(out.SubEvents, SubEventRow{
				ID: newID(), SubscriptionID: subID, EventType: "expired",
				FromStatus: strPtr("active"), ToStatus: "expired", ActorKind: "scheduler", OrderID: nil,
				Payload: string(expPayload), OccurredAt: expAt,
			})
			seData, _ := json.Marshal(map[string]any{"period_end": jsonTime(tl.ends[k])})
			out.Audits = append(out.Audits, AuditRow{
				ActorKind: "system", ActorID: nil, ActorAudience: nil, AuthContext: nil,
				Action: "subscription.expired", ResourceType: "subscription", ResourceID: subID,
				Data: string(seData), OccurredAt: expAt,
			})
			lastEventAt = expAt
		}

		out.Subscriptions = append(out.Subscriptions, SubscriptionRow{
			ID: subID, UserID: userID, PlanID: plan.PlanID, PlanVersionID: plan.VersionID, PriceID: plan.PriceID,
			Status: status, PeriodStart: tl.T0, PeriodEnd: tl.periodEnd,
			Currency: plan.Currency, Amount: plan.Amount, Interval: plan.Interval, IntervalCount: plan.IntervalCount,
			ProxyUUID: uuid.NewString(), CreatedAt: tl.T0, UpdatedAt: lastEventAt,
		})

		sum := make([]int64, k+1)
		if in.HistoryDays > 0 {
			usageEnd := now
			if tl.expired {
				usageEnd = tl.ends[k]
			}
			from := tl.T0
			cut := now.Add(-time.Duration(in.HistoryDays) * 24 * time.Hour)
			if cut.After(from) {
				from = cut
			}
			maxDaily := planMaxDaily(plan)
			d0 := dayOf(from, in.Loc)
			d1 := dayOf(usageEnd, in.Loc)
			for d := d0; !d.After(d1); d = d.AddDate(0, 0, 1) {
				if r.IntN(10) == 0 {
					continue
				}
				bytes := 1 + r.Int64N(maxDaily)
				ds := dayStart(d, in.Loc)
				created := ds
				if tl.T0.After(created) {
					created = tl.T0
				}
				updated := ds.Add(24*time.Hour - time.Second)
				if usageEnd.Before(updated) {
					updated = usageEnd
				}
				if updated.Before(created) {
					updated = created
				}
				t := created
				if ds.After(t) {
					t = ds
				}
				if tl.T0.After(t) {
					t = tl.T0
				}
				pj := periodIndex(t, tl.T0, tl.ends, k)
				sum[pj] += bytes
				out.Usage = append(out.Usage, UsageRow{
					ID: newID(), SubscriptionID: subID, Day: d, Bytes: bytes,
					CreatedAt: created, UpdatedAt: updated,
				})
			}
		}

		var totalTraffic int64
		for _, v := range sum {
			totalTraffic += v
		}
		for _, def := range plan.Quotas {
			var consumed int64
			var pStart time.Time
			var pEnd *time.Time
			switch def.Period {
			case "cycle":
				pStart = tl.cycleStart
				pe := tl.cycleEnd
				pEnd = &pe
				if def.Metric == "traffic.bytes" {
					consumed = sum[k]
				}
			case "total":
				pStart = tl.T0
				pEnd = nil
				if def.Metric == "traffic.bytes" {
					consumed = totalTraffic
				}
			}
			out.Quotas = append(out.Quotas, QuotaRow{
				ID: newID(), SubscriptionID: subID, Metric: def.Metric, Period: def.Period,
				PeriodStart: pStart, PeriodEnd: pEnd, Limit: def.Limit, Consumed: consumed,
				CreatedAt: tl.T0, UpdatedAt: lastEventAt,
			})
		}

		for _, def := range plan.Quotas {
			if def.Period != "cycle" {
				continue
			}
			for j := 1; j <= k; j++ {
				before := int64(0)
				if def.Metric == "traffic.bytes" {
					before = sum[j-1]
				}
				out.ResetLogs = append(out.ResetLogs, ResetLogRow{
					ID: newID(), SubscriptionID: subID, UserID: userID, Metric: def.Metric,
					Reason: "renewal", ConsumedBefore: before, CreatedAt: tl.ends[j-1].Add(5 * time.Minute),
				})
			}
		}
	}

	return out, nil
}
