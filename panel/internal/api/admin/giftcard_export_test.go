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
