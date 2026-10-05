// [INPUT]: 依赖本包商业与内容这一半的 21 个 handler 源文件（按文件名读原文）
// [OUTPUT]: 对外提供 TestBizHandlersRunNoSQL
// [POS]: api/admin 的 SQL 下沉守卫（第二波 api 卫生 apibiz 一路）：这些文件只解析请求、调 domain 服务、写响应，SQL 在拥有那张表的 domain 包；禁用词与 node_routing_notify_test.go 的 TestRoutingHandlersRunNoSQL 一致，三路合完后由总协调收成整包级
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"os"
	"strings"
	"testing"
)

func TestBizHandlersRunNoSQL(t *testing.T) {
	for _, file := range []string{
		"announce.go", "appearance.go", "bulk_users.go", "catalog.go", "commission.go",
		"content.go", "coupon.go", "coupon_batch.go", "giftcard.go", "handlers.go",
		"late_payment.go", "mail.go", "mail_template.go", "revenue.go", "site_settings.go",
		"telegram.go", "ticket_macros.go", "tickets.go", "traffic_packs.go", "traffic_reset.go",
		"usergroup.go",
	} {
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []string{"InTx(", "tx.Query", "tx.Exec", "QueryRow(", "pgx."} {
			if strings.Contains(string(src), banned) {
				t.Errorf("%s must not run SQL directly, found %q", file, banned)
			}
		}
	}
}
