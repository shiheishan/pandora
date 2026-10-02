// [INPUT]: 依赖 giftcard.go 的 writeGiftBatchCSV 与 writeGiftCodesReportCSV，依赖 domain/giftcard 的 BatchExport / CodeReportRow
// [OUTPUT]: 对外提供 TestGiftBatchCSVShape、TestGiftCodesReportCSVShape
// [POS]: api/admin 礼品卡两种 CSV 的形状测试：批次一次性导出（明文）与按筛选的掩码报表（BOM、no-store、列序、防公式）
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package admin

import (
	"bytes"
	"encoding/csv"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/domain/giftcard"
)

func TestGiftBatchCSVShape(t *testing.T) {
	expires := time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	writeGiftBatchCSV(rec, &giftcard.BatchExport{
		Batch: giftcard.Batch{ID: "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000", TemplateName: "双十一"},
		Rows: []giftcard.ExportRow{
			{Code: "GCH2K9QRSTUVWX", Status: "unused", ExpiresAt: &expires, TemplateName: "双十一"},
			{Code: "GCABCDEFGHJKMN", Status: "used", TemplateName: "双十一"},
		},
	})
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="gift-codes-0199aaaa.csv"` {
		t.Fatalf("Content-Disposition=%q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, plaintext codes must not be cached", got)
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("CSV must start with a UTF-8 BOM so Excel shows Chinese headers")
	}
	records, err := csv.NewReader(bytes.NewReader(body[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"卡密", "状态", "有效期", "模板"},
		{"GCH2K9QRSTUVWX", "unused", "2026-12-31 23:59", "双十一"},
		{"GCABCDEFGHJKMN", "used", "", "双十一"},
	}
	if len(records) != len(want) {
		t.Fatalf("records=%v", records)
	}
	for i := range want {
		for j := range want[i] {
			if records[i][j] != want[i][j] {
				t.Fatalf("row %d=%v want %v", i, records[i], want[i])
			}
		}
	}
}

// 掩码报表：BOM、不缓存、表头与列序，自由文本列防公式；行里只有掩码。
func TestGiftCodesReportCSVShape(t *testing.T) {
	batch := "0199aaaa-bbbb-7ccc-8ddd-eeeeffff0000"
	created := time.Date(2026, 9, 1, 8, 0, 0, 0, time.UTC)
	used := time.Date(2026, 9, 2, 9, 30, 0, 0, time.UTC)
	rec := httptest.NewRecorder()
	writeGiftCodesReportCSV(rec, []giftcard.CodeReportRow{
		{CodeMasked: "GCH2K9••••••••", Status: "used", TemplateName: "双十一", BatchID: &batch,
			CreatedAt: created, UsedEmail: "=cmd@example.test", UsedAt: &used},
		{CodeMasked: "GCABCD••••••••", Status: "unused", TemplateName: "+活动", CreatedAt: created},
	}, time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC))
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="gift-codes-report-20261001-120000.csv"` {
		t.Fatalf("Content-Disposition=%q", got)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("report must not be cached")
	}
	body := rec.Body.Bytes()
	if !bytes.HasPrefix(body, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("CSV must start with a UTF-8 BOM")
	}
	records, err := csv.NewReader(bytes.NewReader(body[3:])).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"卡密（掩码）", "状态", "模板", "批次", "有效期", "生成时间", "兑换人", "兑换时间"},
		{"GCH2K9••••••••", "used", "双十一", batch, "", "2026-09-01 08:00", "'=cmd@example.test", "2026-09-02 09:30"},
		{"GCABCD••••••••", "unused", "'+活动", "", "", "2026-09-01 08:00", "", ""},
	}
	if len(records) != len(want) {
		t.Fatalf("records=%v", records)
	}
	for i := range want {
		for j := range want[i] {
			if records[i][j] != want[i][j] {
				t.Fatalf("row %d=%v want %v", i, records[i], want[i])
			}
		}
	}
}
