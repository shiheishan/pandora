// [INPUT]: 依赖 catalog.go 的 loadPlanTx、prepare*Input 校验与 updatePlanTx/createPlanVersionTx/updatePlanVersionTx/publishPlanVersionTx 事务体，依赖 plan_wizard.go 的 bindPoolsTx，依赖 platform/audit、platform/db、platform/httpx
// [OUTPUT]: 对外提供 UpdatePlanComplete、UpdatePlanCompleteInput/Output 与三态 OptionalInt；包内 inheritVersionSemantics
// [POS]: adminops 套餐向导的「一次改完」：把资料、价格、额度与线路编排进同一个事务；设备数与限速三态、新版本继承当前版本全部高级设置、资料写入保留上架时间窗（R92）；plan_wizard.go 是它的「一次建成」兄弟
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package adminops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/audit"
	"github.com/aegispanel/aegis/internal/platform/db"
	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// 改套餐也一次改完，不用在四个弹窗之间跳。
//
// 对着 xboard 看的：它的套餐是一张扁平表 —— 价格是 JSON 字段、节点分组
// 是单个 group_id、没有版本概念，所以一个 save 就是一次 UPDATE。
//
// 我们的模型是六张表（products / plans / plan_versions / prices /
// plan_node_pools / quota_definitions），换来的是版本化：已经买了的用户
// 按购买那一刻的版本执行，之后改流量不会把他们的额度改掉。这个能力不能
// 为了少几个接口丢掉。
//
// 但界面没必要跟着表结构走。这里把「改基本资料、改流量与设备数、改价格、
// 改节点分组」编排成一次调用，版本怎么开、旧价格怎么归档，都在后面处理。
//
// 各字段的生效方式不一样，这不是实现细节，是业务语义：
//
//   - 基本资料（名字、可见性、开关）—— 直接改，立即生效
//   - 流量 / 设备数 —— 开新版本。已购用户留在旧版本上，额度不受影响
//   - 价格 —— 归档旧的、建新的。已经付过钱的订单不受影响
//   - 节点分组 —— 同样要开新版本。已发布的版本是冻结的，它的配额和
//     分组绑定都不可改（"only draft versions can modify node pools"），
//     这是快照语义的一部分：已购用户看到的东西不能被事后改动。
//
// 最后一条和 xboard 不一样，值得说清楚：xboard 的套餐是扁平表，改了
// group_id 所有人立刻换线路。我们做不到「立刻对所有人生效」——那正是
// 版本化换来的保护的另一面。加了新线路，已购用户要到续费换版本时才拿
// 得到。这个差异不能靠界面掩盖，得如实写在返回的说明里。
//
// 整个编排在一个事务里（缺陷 12）：此前资料、价格、版本各自提交，后一步
// 失败时前面已生效的部分不回滚，管理员看到一个报错、库里却是改了一半的
// 套餐。现在任何一步失败，套餐保持提交前的样子。

type UpdatePlanCompleteInput struct {
	ActorID string `json:"-"`

	// ExpectedRowVersion 用于乐观锁，来自详情接口。
	ExpectedRowVersion int64 `json:"expected_row_version"`

	// --- 基本资料 ---
	Code        string  `json:"code"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Visibility  string  `json:"visibility"`
	SortOrder   int     `json:"sort_order"`

	AllowNewPurchase     *bool    `json:"allow_new_purchase"`
	AllowRenewal         *bool    `json:"allow_renewal"`
	AllowUpgrade         *bool    `json:"allow_upgrade"`
	VisibleGroupIDs      []string `json:"visible_group_ids"`
	PurchaseLimitPerUser *int     `json:"purchase_limit_per_user"`
	StockTotal           *int     `json:"stock_total"`
	// 卖点与推荐（R100）：为 nil 表示这次不动
	Highlights  *[]string `json:"highlights"`
	Recommended *bool     `json:"recommended"`

	// --- 卖的是什么 ---
	// TrafficGB 为 nil 表示这次不动它，0 表示不限。
	TrafficGB *int64 `json:"traffic_gb"`
	// MaxDevices / ThrottleKbps 是三态（R92、R99）：字段缺省 = 不动，显式 null =
	// 清为不限，正整数 = 设置。*int 分不出前两种，向导因此改不回「不限设备」。
	MaxDevices   OptionalInt `json:"max_devices"`
	ThrottleKbps OptionalInt `json:"throttle_kbps"`

	// --- 价格与分组。为 nil 表示不动 ---
	// Prices 只管清单里出现的币种的公开价（不绑用户组）：同币种清单外的
	// 在售公开价归档，其余币种与用户组专属价一律不动 —— 向导表达不了它们，
	// 就不该替管理员删掉它们。空数组等同于不动。
	Prices *[]PlanPriceInput `json:"prices"`
	// PoolIDs 给了空数组表示清空线路。
	PoolIDs *[]string `json:"pool_ids"`
}

// OptionalInt 区分「字段缺省」与「显式 null」：Set 为假是缺省，Set 为真且
// Value 为 nil 是 null。同 nodefabric.OptionalNullableString 的做法。
type OptionalInt struct {
	Set   bool
	Value *int
}

func (o *OptionalInt) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Value = nil
		return nil
	}
	var v int
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// resolve 把三态落成最终值：缺省沿用 current，其余取请求值（nil = 不限）。
func (o OptionalInt) resolve(current *int) *int {
	if !o.Set {
		return current
	}
	return o.Value
}

type UpdatePlanCompleteOutput struct {
	Plan *CatalogPlanDetail `json:"plan"`
	// Changed 逐条说明这次实际改了什么、什么时候生效。
	// 界面直接把它显示给用户 —— 「保存成功」说明不了额度是立刻变了
	// 还是只对新用户生效。
	Changed []string `json:"changed"`
}

// UpdatePlanComplete 一次改完套餐的资料、额度、价格与节点分组，全部在一个事务里。
func (s *Service) UpdatePlanComplete(ctx context.Context, tenantID, planID string,
	in UpdatePlanCompleteInput) (*UpdatePlanCompleteOutput, error) {

	// 能在事务外判的都先判：失败时一行都不碰，也不占套餐行锁。
	planInput := UpdatePlanInput{
		ActorID: in.ActorID, ExpectedRowVersion: in.ExpectedRowVersion,
		Code: in.Code, Name: in.Name, Description: in.Description,
		Visibility: in.Visibility, VisibleGroupIDs: in.VisibleGroupIDs,
		PurchaseLimitPerUser: in.PurchaseLimitPerUser,
		StockTotal:           in.StockTotal, SortOrder: in.SortOrder,
	}
	if in.Highlights != nil {
		planInput.Highlights = *in.Highlights
	}
	if err := prepareUpdatePlanInput(planID, &planInput); err != nil {
		return nil, err
	}
	if err := validateWizardQuotaEdits(in); err != nil {
		return nil, err
	}
	var wantPrices map[string]PlanPriceInput
	if in.Prices != nil && len(*in.Prices) > 0 {
		var err error
		if wantPrices, err = preparePlanPrices(*in.Prices, in.ActorID); err != nil {
			return nil, err
		}
		if err := s.requireP0BSales(); err != nil {
			return nil, err
		}
	}

	var out *CatalogPlanDetail
	var changed []string
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID, ActorID: in.ActorID}, func(tx pgx.Tx) error {
		changed = []string{}
		// 先锁套餐行再读现状：目录的每个写用例都先锁这一行，
		// 读到的版本、价格与分组在本事务结束前不会被别人改掉。
		if _, err := tx.Exec(ctx, `SELECT 1 FROM plans
			WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID); err != nil {
			return err
		}
		before := &CatalogPlanDetail{Versions: []VersionRow{}, Prices: []PriceRow{}}
		if err := loadPlanTx(ctx, tx, tenantID, planID, before); err != nil {
			return err
		}

		// 1) 基本资料。UpdatePlanInput 这三个是 bool 不是 *bool：传 nil 表示
		// 「这次不动它」，所以要从当前值兜底 —— 直接取零值会把「允许续费」悄悄关掉。
		planInput.AllowNewPurchase = boolOr(in.AllowNewPurchase, before.AllowNewPurchase)
		planInput.AllowRenewal = boolOr(in.AllowRenewal, before.AllowRenewal)
		planInput.AllowUpgrade = boolOr(in.AllowUpgrade, before.AllowUpgrade)
		// 向导没有上架时间窗的输入，资料又是整体写入：不从现状带上，窗口就被
		// 清成「永远可见」（R92 ③）。时间窗只在「销售设置」里改。
		planInput.VisibleFrom, planInput.VisibleUntil = before.VisibleFrom, before.VisibleUntil
		// 卖点与推荐缺省 = 不动（R100）；给了的已在事务外校验并规整过。
		if in.Highlights == nil {
			planInput.Highlights = before.Highlights
		}
		planInput.Recommended = boolOr(in.Recommended, before.Recommended)
		if _, err := s.updatePlanTx(ctx, tx, tenantID, planID, planInput); err != nil {
			return err
		}
		changed = append(changed, "套餐资料已更新")

		// 2) 价格先于版本：下面发布新版本时的「有可售价格」校验看到的是最终价格。
		priceChanges := 0
		if len(wantPrices) > 0 {
			var err error
			if priceChanges, err = syncPlanPricesTx(ctx, tx, tenantID, planID,
				before.ProductID, wantPrices, in.ActorID); err != nil {
				return fmt.Errorf("更新价格: %w", err)
			}
		}

		// 3) 额度与线路：合并进同一个新版本。
		//
		// 两者都存在已发布版本上，而已发布的版本是冻结的、子对象不可改。
		// 第一版是先滚版本改额度、再去改分组，结果分组那步撞上冻结保护：
		//   "plan version ... was frozen; its snapshot children are immutable"
		// 而且就算能改，分两次滚版本也会平白多出一个中间版本。
		quotaChanged := quotaDiffers(before, in)
		poolsChanged := in.PoolIDs != nil && poolsDiffer(before, *in.PoolIDs)
		if quotaChanged || poolsChanged {
			if err := s.rollPlanVersionTx(ctx, tx, tenantID, planID, before, in); err != nil {
				return fmt.Errorf("更新额度与线路: %w", err)
			}
			if quotaChanged {
				changed = append(changed,
					"流量、设备数与限速已更新；新购买的用户按新额度，已经买了的用户仍按原额度")
			}
			if poolsChanged {
				changed = append(changed,
					"可用线路已更新；新购买的用户立即拿到，已经买了的用户要到续费时才切过来")
			}
		}
		if priceChanges > 0 {
			changed = append(changed,
				"价格已更新，只影响之后的新购与续费；已成交的订单不变")
		}

		out = &CatalogPlanDetail{Versions: []VersionRow{}, Prices: []PriceRow{}}
		return loadPlanTx(ctx, tx, tenantID, planID, out)
	})
	if err != nil {
		return nil, catalogResult(err)
	}
	return &UpdatePlanCompleteOutput{Plan: out, Changed: changed}, nil
}

// validateWizardQuotaEdits 在进事务前拦掉额度字段的非法值，字段键即请求字段名。
func validateWizardQuotaEdits(in UpdatePlanCompleteInput) error {
	fields := map[string]string{}
	if in.TrafficGB != nil && *in.TrafficGB < 0 {
		fields["traffic_gb"] = "流量不能是负数；不限流量填 0"
	}
	if in.MaxDevices.Value != nil && *in.MaxDevices.Value <= 0 {
		fields["max_devices"] = "必须为正整数；不限设备请传 null"
	}
	if in.ThrottleKbps.Value != nil && *in.ThrottleKbps.Value <= 0 {
		fields["throttle_kbps"] = "必须为正整数；不限速请传 null"
	}
	if len(fields) > 0 {
		return httpx.Invalid(fields)
	}
	return nil
}

// quotaDiffers 判断这次提交有没有真的改动额度。
func quotaDiffers(before *CatalogPlanDetail, in UpdatePlanCompleteInput) bool {
	if in.TrafficGB == nil && !in.MaxDevices.Set && !in.ThrottleKbps.Set {
		return false
	}
	cur := currentVersion(before)
	if cur == nil {
		return true
	}
	if in.MaxDevices.Set && !sameIntPtr(cur.MaxDevices, in.MaxDevices.Value) {
		return true
	}
	if in.ThrottleKbps.Set && !sameIntPtr(cur.ThrottleKbps, in.ThrottleKbps.Value) {
		return true
	}
	if in.TrafficGB != nil {
		// 没有流量行或行上 limit 为空都是不限，与请求里的 0 同义；按 -1 起算的话，
		// 每次原样提交「不限」都会白白滚出一个新版本。
		var curGB int64
		for _, q := range cur.Quotas {
			if q.Metric == "traffic.bytes" && q.Limit != nil {
				curGB = *q.Limit / bytesPerGB
			}
		}
		if curGB != *in.TrafficGB {
			return true
		}
	}
	return false
}

func currentVersion(p *CatalogPlanDetail) *VersionRow {
	if p == nil || p.CurrentVersionID == nil {
		return nil
	}
	for i := range p.Versions {
		if p.Versions[i].ID == *p.CurrentVersionID {
			return &p.Versions[i]
		}
	}
	return nil
}

// poolsDiffer 判断线路清单有没有真的变化。
// 没变就别滚版本 —— 每保存一次多一个版本，列表很快没法看。
func poolsDiffer(before *CatalogPlanDetail, want []string) bool {
	cur := currentVersion(before)
	have := []string{}
	if cur != nil {
		have = cur.PoolIDs
	}
	if len(have) != len(want) {
		return true
	}
	seen := map[string]bool{}
	for _, x := range have {
		seen[x] = true
	}
	for _, x := range want {
		if !seen[x] {
			return true
		}
	}
	return false
}

func boolOr(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func sameIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// pricesKey 把一档价格压成可比较的键：周期 + 币种唯一确定一档公开价。
func pricesKey(interval string, count int16, currency string) string {
	return fmt.Sprintf("%s/%d/%s", interval, count, currency)
}

// preparePlanPrices 在进事务之前校验清单，返回 键 → 价格。
func preparePlanPrices(want []PlanPriceInput, actorID string) (map[string]PlanPriceInput, error) {
	wanted := map[string]PlanPriceInput{}
	for _, p := range want {
		candidate := CreatePriceInput{
			ActorID: actorID, Currency: p.Currency, UnitAmount: p.UnitAmount,
			BillingInterval: p.BillingInterval, IntervalCount: p.IntervalCount,
			TrialDays: p.TrialDays,
		}
		if err := validatePrice(candidate); err != nil {
			return nil, err
		}
		wanted[pricesKey(p.BillingInterval, p.IntervalCount, p.Currency)] = p
	}
	return wanted, nil
}

// priceSyncCurrencies 是本次同步管得着的币种：只有清单里出现过的。
func priceSyncCurrencies(wanted map[string]PlanPriceInput) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range wanted {
		if !seen[p.Currency] {
			seen[p.Currency] = true
			out = append(out, p.Currency)
		}
	}
	sort.Strings(out)
	return out
}

// syncPlanPricesTx 让清单币种内的在售公开价和提交的清单一致，返回改动条数。
//
// 不做「原地改金额」—— prices 表的一行会被历史订单引用，改掉它等于篡改
// 已成交订单的单价。所以是归档旧的、建新的：老订单仍指向老那一行。
//
// 范围只到「清单里出现的币种 × 不绑用户组的公开价」（缺陷 12）：向导只回传
// 人民币时，此前会把美元价和用户组专属价一起归档。
func syncPlanPricesTx(ctx context.Context, tx pgx.Tx, tenantID, planID, productID string,
	wanted map[string]PlanPriceInput, actorID string) (int, error) {

	currencies := priceSyncCurrencies(wanted)
	if len(currencies) == 0 {
		return 0, nil
	}
	var status string
	if err := tx.QueryRow(ctx, `SELECT status FROM plans
		WHERE tenant_id=$1 AND id=$2::uuid FOR UPDATE`, tenantID, planID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, httpx.NotFoundOrForbidden()
		}
		return 0, err
	}
	if status == "archived" {
		return 0, httpx.New(httpx.CodeConflict, "已归档套餐不能更新价格")
	}

	have := map[string]PriceRow{}
	rows, err := tx.Query(ctx, `SELECT id,currency,unit_amount,billing_interval,
		interval_count,trial_days,status,user_group_id,valid_from,valid_until,row_version
		FROM prices WHERE tenant_id=$1 AND product_id=$2::uuid AND status='active'
		  AND user_group_id IS NULL AND currency::text = ANY($3::text[])
		ORDER BY id FOR UPDATE`, tenantID, productID, currencies)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var p PriceRow
		if err := rows.Scan(&p.ID, &p.Currency, &p.UnitAmount, &p.Interval, &p.Count,
			&p.TrialDays, &p.Status, &p.UserGroupID, &p.ValidFrom, &p.ValidUntil,
			&p.RowVersion); err != nil {
			rows.Close()
			return 0, err
		}
		have[pricesKey(p.Interval, p.Count, p.Currency)] = p
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	changes := 0
	keys := make([]string, 0, len(have))
	for k := range have {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		old := have[k]
		w, keep := wanted[k]
		if keep && w.UnitAmount == old.UnitAmount && w.TrialDays == old.TrialDays {
			continue
		}
		if _, err := tx.Exec(ctx, `UPDATE prices SET status='archived',
			row_version=row_version+1 WHERE tenant_id=$1 AND id=$2::uuid`,
			tenantID, old.ID); err != nil {
			return 0, err
		}
		changes++
	}

	wkeys := make([]string, 0, len(wanted))
	for k := range wanted {
		wkeys = append(wkeys, k)
	}
	sort.Strings(wkeys)
	for _, k := range wkeys {
		w := wanted[k]
		old, exists := have[k]
		if exists && w.UnitAmount == old.UnitAmount && w.TrialDays == old.TrialDays {
			continue
		}
		if _, err := tx.Exec(ctx, `INSERT INTO prices
			(tenant_id,product_id,currency,unit_amount,billing_interval,
			 interval_count,trial_days,status)
			VALUES($1,$2::uuid,$3,$4,$5,$6,$7,'active')`,
			tenantID, productID, w.Currency, w.UnitAmount, w.BillingInterval,
			w.IntervalCount, w.TrialDays); err != nil {
			return 0, err
		}
		changes++
	}
	if changes == 0 {
		return 0, nil
	}
	return changes, audit.Write(ctx, tx, tenantID, audit.Entry{
		ActorKind: "admin", ActorID: &actorID, Action: "price.sync",
		ResourceType: "plan", ResourceID: &planID, APIDomain: "admin",
		Outcome: "success", RequestID: httpx.RequestIDFrom(ctx),
		AfterDigest: map[string]any{"changes": changes, "currencies": currencies},
	})
}

// rollPlanVersionTx 开一个新版本承载新的额度与线路，然后发布。
//
// 已经买了的用户留在旧版本上 —— 这正是版本存在的意义：改流量不该把
// 别人已经付过钱的额度改掉。
func (s *Service) rollPlanVersionTx(ctx context.Context, tx pgx.Tx, tenantID, planID string,
	before *CatalogPlanDetail, in UpdatePlanCompleteInput) error {

	// 发布受销售开关控制；在动任何版本之前就判掉。
	if err := s.requireP0BSales(); err != nil {
		return err
	}
	cur := currentVersion(before)

	// 复用已有的 draft（每个套餐最多一个）；没有就建。套餐行已被本事务锁住，
	// 不会有并发的第二个 draft 冒出来。事务失败时新建的 draft 一并回滚。
	var ver *VersionRow
	for i := range before.Versions {
		if before.Versions[i].Status == "draft" && before.Versions[i].FrozenAt == nil {
			ver = &before.Versions[i]
			break
		}
	}
	reused := ver != nil
	if !reused {
		var err error
		if ver, err = s.createPlanVersionTx(ctx, tx, tenantID, planID, in.ActorID); err != nil {
			return err
		}
	}

	semantics := inheritVersionSemantics(cur, in)
	semantics.ActorID, semantics.ExpectedRowVersion = in.ActorID, ver.RowVersion
	if err := prepareVersionSemanticsInput(planID, ver.ID, semantics); err != nil {
		return err
	}
	if _, err := s.updatePlanVersionTx(ctx, tx, tenantID, planID, ver.ID, semantics); err != nil {
		return err
	}

	// 线路：这次给了就用新的，没给就沿用旧版本的。
	//
	// 沿用那一条不能省：只想改个流量却把线路丢了，发布之后所有人的订阅
	// 会瞬间变空。
	poolIDs := []string{}
	if in.PoolIDs != nil {
		poolIDs = *in.PoolIDs
	} else if cur != nil {
		poolIDs = cur.PoolIDs
	}
	if reused {
		// 复用的 draft 可能带着之前的绑定，先清空；空数组也必须清空。
		if _, err := tx.Exec(ctx, `DELETE FROM plan_node_pools
			WHERE tenant_id=$1 AND plan_version_id=$2::uuid`, tenantID, ver.ID); err != nil {
			return err
		}
	}
	if _, err := bindPoolsTx(ctx, tx, tenantID, ver.ID, poolIDs); err != nil {
		return err
	}

	// 发布用的两个乐观锁令牌就地读：本事务刚把它们各推进了若干格，
	// 手工记账容易漏一格。两行都已被本事务锁住，读到的就是最终值。
	var planRow, versionRow int64
	if err := tx.QueryRow(ctx, `SELECT p.row_version, v.row_version
		FROM plans p JOIN plan_versions v ON v.tenant_id=p.tenant_id AND v.plan_id=p.id
		WHERE p.tenant_id=$1 AND p.id=$2::uuid AND v.id=$3::uuid`,
		tenantID, planID, ver.ID).Scan(&planRow, &versionRow); err != nil {
		return err
	}
	_, _, err := s.publishPlanVersionTx(ctx, tx, tenantID, planID, ver.ID,
		in.ActorID, planRow, versionRow)
	return err
}

// inheritVersionSemantics 以当前版本为底稿拼出新版本的语义，只换向导这次
// 改了的额度（R92 ②）。
//
// 此前新版本除了额度一律取默认值：宽限期归零、续费语义回到默认、权益整批
// 丢失——管理员只改了个流量，已配好的高级设置就悄悄没了。现在凡是向导
// 表达不了的都原样继承：重置策略与日、宽限、续费三项、并发与设备释放、
// 备注、权益，以及流量与设备以外的配额行。
//
// 例外只有超额策略：新写入只收 suspend（R99）。旧版本若是 throttle，它的
// 速率照样继承——限速本来就全程生效，与策略无关。
func inheritVersionSemantics(cur *VersionRow, in UpdatePlanCompleteInput) VersionSemanticsInput {
	out := VersionSemanticsInput{
		QuotaResetStrategy: "billing_cycle",
		GraceKeepsService:  true, RenewalExtendsPeriod: true,
		RenewalResetsQuota: true, RenewalKeepsAddons: true,
		OveragePolicy: "suspend",
		Entitlements:  []EntitlementInput{},
	}
	var curQuotas []QuotaInput
	var curDevices, curThrottle *int
	if cur != nil {
		if cur.QuotaResetStrategy != "" {
			out.QuotaResetStrategy = cur.QuotaResetStrategy
			out.QuotaResetDay = cur.QuotaResetDay
		}
		out.GracePeriodHours = cur.GracePeriodHours
		out.GraceKeepsService = cur.GraceKeepsService
		out.RenewalExtendsPeriod = cur.RenewalExtendsPeriod
		out.RenewalResetsQuota = cur.RenewalResetsQuota
		out.RenewalKeepsAddons = cur.RenewalKeepsAddons
		out.MaxConcurrent = cur.MaxConcurrent
		out.DeviceReleaseHours = cur.DeviceReleaseHours
		out.Notes = cur.Notes
		out.Entitlements = append(out.Entitlements, cur.Entitlements...)
		curQuotas, curDevices, curThrottle = cur.Quotas, cur.MaxDevices, cur.ThrottleKbps
	}
	out.MaxDevices = in.MaxDevices.resolve(curDevices)
	out.ThrottleKbps = in.ThrottleKbps.resolve(curThrottle)

	// 配额行：流量没改就整行照抄（不经 GB 换算，免得非整 GB 的额度被取整）；
	// 改了就按新值重写，0 = 不限、不写行。设备行始终跟着最终的 max_devices。
	out.Quotas = []QuotaInput{}
	trafficPeriod, devicesPeriod := "cycle", "cycle"
	for _, q := range curQuotas {
		switch q.Metric {
		case "traffic.bytes":
			if in.TrafficGB == nil {
				out.Quotas = append(out.Quotas, q)
			} else {
				trafficPeriod = q.Period
			}
		case "devices.active":
			devicesPeriod = q.Period
		default:
			out.Quotas = append(out.Quotas, q)
		}
	}
	if in.TrafficGB != nil && *in.TrafficGB > 0 {
		out.Quotas = append(out.Quotas, QuotaInput{
			Metric: "traffic.bytes", Limit: ptrInt64(*in.TrafficGB * bytesPerGB),
			Unit: "bytes", Period: trafficPeriod})
	}
	if out.MaxDevices != nil {
		out.Quotas = append(out.Quotas, QuotaInput{
			Metric: "devices.active", Limit: ptrInt64(int64(*out.MaxDevices)),
			Unit: "count", Period: devicesPeriod})
	}
	return out
}
