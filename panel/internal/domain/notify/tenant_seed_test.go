package notify

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 新租户的模板来自建租户触发器，老租户的来自当年的种子迁移，「恢复默认」
// 回到 defaultTemplates。三份文本必须一致，否则同一个模板在不同租户、
// 或者恢复默认前后是两份文案。
func TestTenantSeedTemplatesMatchDefaults(t *testing.T) {
	seed := latestTenantSeedMigration(t)
	for key, d := range defaultTemplates {
		code, channel, _ := strings.Cut(key, "|")
		row := "('" + code + "', '" + channel + "',"
		if !strings.Contains(seed, row) {
			t.Errorf("tenant seed lacks %s", key)
			continue
		}
		if !strings.Contains(seed, "'"+d.Subject+"'") || !strings.Contains(seed, "'"+d.Body+"'") {
			t.Errorf("tenant seed text for %s drifted from defaultTemplates", key)
		}
	}
	// defaultTemplates 不含 Telegram 与群发，这里单独点名，凑齐 18 个
	for _, key := range []string{
		"subscription.expiring|telegram", "quota.warning|telegram", "order.paid|telegram",
		"ticket.replied|telegram", "admin.broadcast|email",
		"subscription.expired|telegram", "subscription.recall|telegram",
	} {
		code, channel, _ := strings.Cut(key, "|")
		if !strings.Contains(seed, "('"+code+"', '"+channel+"',") {
			t.Errorf("tenant seed lacks %s", key)
		}
	}
	if n := len(defaultTemplates) + 7; n != 18 {
		t.Fatalf("built-in template count=%d; update the seed trigger and this test together", n)
	}
}

// latestTenantSeedMigration 按文件名排序取最后一个定义 seed 函数的迁移：
// 以后若有迁移 CREATE OR REPLACE 它，核对的就是生效的那一版。
func latestTenantSeedMigration(t *testing.T) string {
	t.Helper()
	files, err := filepath.Glob("../../../migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	latest := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "FUNCTION app.seed_tenant_defaults(") {
			latest = string(raw)
		}
	}
	if latest == "" {
		t.Fatal("no migration defines app.seed_tenant_defaults")
	}
	return latest
}
