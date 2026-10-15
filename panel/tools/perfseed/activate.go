package main

import (
	"context"
	"fmt"
	"path/filepath"
)

// perfPrice 是建套餐请求里的一档价格（unit_amount 单位是分）。
type perfPrice struct {
	BillingInterval string `json:"billing_interval"`
	IntervalCount   int    `json:"interval_count"`
	UnitAmount      int64  `json:"unit_amount"`
	Currency        string `json:"currency"`
}

// perfPlan 是 activate 建的一个套餐；TrafficGB 为空 = 不限量。
type perfPlan struct {
	Code      string
	Name      string
	TrafficGB *int64
	Prices    []perfPrice
}

// perfActivatePlans 是 activate 建的两个套餐，顺序固定（users 按用户下标轮流分配）。
var perfActivatePlans = []perfPlan{
	{
		Code:      "perf-basic",
		Name:      "Perf 基础月付",
		TrafficGB: int64Ptr(200),
		Prices:    []perfPrice{{BillingInterval: "month", IntervalCount: 1, UnitAmount: 1500, Currency: "CNY"}},
	},
	{
		Code: "perf-unlimited",
		Name: "Perf 不限量",
		Prices: []perfPrice{
			{BillingInterval: "month", IntervalCount: 1, UnitAmount: 3000, Currency: "CNY"},
			{BillingInterval: "year", IntervalCount: 1, UnitAmount: 30000, Currency: "CNY"},
		},
	},
}

func int64Ptr(v int64) *int64 { return &v }

// runActivate 让 infra 建的节点逐个上线，再建并发布套餐写 catalog.json；已做过的部分跳过。
func runActivate(ctx context.Context, o activateOpts) error {
	infraPath := filepath.Join(o.OutDir, fileInfra)
	catalogPath := filepath.Join(o.OutDir, fileCatalog)

	var state InfraState
	exists, err := loadJSON(infraPath, &state)
	if err != nil {
		return err
	}
	if !exists || !state.TokensDone {
		return fmt.Errorf("先跑 perfseed infra")
	}

	pass, err := readPasswordFile(o.AdminPasswordFile)
	if err != nil {
		return err
	}
	cli := newAdminClient(o.AdminURL, o.AdminEmail, pass)
	if err := cli.auth(ctx); err != nil {
		return err
	}

	n := len(state.Servers)
	for i := 0; i < n; i++ {
		sv := &state.Servers[i]
		if sv.Activated {
			continue
		}
		var out struct {
			ServingStatus string `json:"serving_status"`
		}
		path := fmt.Sprintf("/v1/nodes/%s/activate", sv.NodeID)
		idem := fmt.Sprintf("perfseed-act-%s", sv.NodeID)
		body := map[string]int64{"row_version": sv.NodeRowVersion}
		if err := cli.do(ctx, "POST", path, body, idem, 200, &out); err != nil {
			return fmt.Errorf("节点 %s 上线失败（服务器要先被 nodesim 接入并保持 connected）：%w", sv.Name, err)
		}
		if out.ServingStatus != "active" {
			return fmt.Errorf("节点 %s 上线失败（服务器要先被 nodesim 接入并保持 connected）：serving_status=%s", sv.Name, out.ServingStatus)
		}
		sv.Activated = true
		if err := saveJSON(infraPath, state); err != nil {
			return err
		}
		idx := i + 1
		if idx%20 == 0 || idx == n {
			fmt.Printf("节点上线 %d/%d\n", idx, n)
		}
	}

	if fileExists(catalogPath) {
		fmt.Printf("套餐已经建过（%s），跳过\n", catalogPath)
		fmt.Println("activate 完成，下一步 perfseed users")
		return nil
	}

	if err := cli.auth(ctx); err != nil {
		return err
	}

	var catalog Catalog
	for _, spec := range perfActivatePlans {
		plan, err := createCompletePlan(ctx, cli, state.PoolID, spec)
		if err != nil {
			return err
		}
		catalog.Plans = append(catalog.Plans, plan)
		fmt.Printf("套餐 %s plan_id=%s\n", plan.Code, plan.PlanID)
	}
	if err := saveJSON(catalogPath, catalog); err != nil {
		return err
	}

	fmt.Println("activate 完成，下一步 perfseed users")
	return nil
}

// createCompletePlan 一步建好并发布一个套餐（POST /v1/plans/complete），返回写进 catalog.json 的那一项。
func createCompletePlan(ctx context.Context, cli *adminClient, poolID string, spec perfPlan) (CatalogPlan, error) {
	body := map[string]any{
		"code":       spec.Code,
		"name":       spec.Name,
		"visibility": "public",
		"pool_ids":   []string{poolID},
		"prices":     spec.Prices,
		"publish":    true,
	}
	if spec.TrafficGB != nil {
		body["traffic_gb"] = *spec.TrafficGB
	}
	var out struct {
		Plan struct {
			ID string `json:"id"`
		} `json:"plan"`
		VersionID string   `json:"version_id"`
		PriceIDs  []string `json:"price_ids"`
	}
	idem := "perfseed-plan-" + spec.Code
	if err := cli.do(ctx, "POST", "/v1/plans/complete", body, idem, 201, &out); err != nil {
		return CatalogPlan{}, err
	}
	if out.Plan.ID == "" || out.VersionID == "" || len(out.PriceIDs) != len(spec.Prices) {
		return CatalogPlan{}, fmt.Errorf("套餐 %s：响应缺 plan.id、version_id，或 price_ids 数目与请求不符", spec.Code)
	}
	cp := CatalogPlan{
		Code:      spec.Code,
		Name:      spec.Name,
		PlanID:    out.Plan.ID,
		VersionID: out.VersionID,
	}
	if spec.TrafficGB != nil {
		cp.TrafficGB = *spec.TrafficGB
	}
	for i, p := range spec.Prices {
		cp.Prices = append(cp.Prices, CatalogPrice{
			PriceID:         out.PriceIDs[i],
			BillingInterval: p.BillingInterval,
			IntervalCount:   p.IntervalCount,
			Currency:        p.Currency,
			UnitAmountCents: p.UnitAmount,
		})
	}
	return cp, nil
}
