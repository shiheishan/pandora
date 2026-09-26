// [INPUT]: 依赖 MaskCode、读模型 Code / Usage 与 generateSampleSize，依赖 platform/sourcetest 按名取 Service.GenerateCodes 与 Service.ExportBatch 的源码
// [OUTPUT]: 对外提供 TestMaskCodeShowsPrefixAndFourRandomCharacters、TestGiftCardReadModelsCarryOnlyMaskedCodes、TestGenerateCodesKeepsOnlyASmallPlaintextSample、TestExportBatchMarksReadsAndAuditsInOneTransaction
// [POS]: giftcard 礼品卡码的脱敏、生成只回少量明文样本、一次性导出在一个事务里且审计不带码
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/sourcetest"
)

func TestMaskCodeShowsPrefixAndFourRandomCharacters(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		// 前缀 GC + 12 位随机段：露出前缀与随机段前 4 位
		{"GCH2K9QRSTUVWX", "GCH2K9••••••••"},
		// 无前缀：只露随机段前 4 位
		{"ABCDEFGHJKMN", "ABCD••••••••"},
		// 长度不超过遮挡位数时全部遮住，不能因为太短反而露出全文
		{"ABCDEFGH", "••••••••"},
		{"", ""},
	} {
		if got := MaskCode(tc.in); got != tc.want {
			t.Errorf("MaskCode(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

// 后台列表、兑换记录与生码响应都不能再出现完整明文（契约 后台-06）。
func TestGiftCardReadModelsCarryOnlyMaskedCodes(t *testing.T) {
	for name, value := range map[string]any{
		"code":  Code{CodeMasked: "GC••••"},
		"usage": Usage{CodeMasked: "GC••••"},
	} {
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(body, &fields); err != nil {
			t.Fatal(err)
		}
		if _, ok := fields["code"]; ok {
			t.Errorf("%s still exposes a plaintext code field: %s", name, body)
		}
		if _, ok := fields["code_masked"]; !ok {
			t.Errorf("%s lacks code_masked: %s", name, body)
		}
	}

	body, err := json.Marshal(GenerateOutput{BatchID: "b", Count: 100,
		Sample: []string{"A", "B", "C", "D"}, Batch: Batch{ID: "b", CreatedAt: time.Now()}})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"batch_id", "count", "sample", "batch"} {
		if _, ok := out[key]; !ok {
			t.Errorf("generate response lacks %q: %s", key, body)
		}
	}
	if _, ok := out["codes"]; ok {
		t.Fatalf("generate response must not return the whole batch in plaintext: %s", body)
	}
}

func TestGenerateCodesKeepsOnlyASmallPlaintextSample(t *testing.T) {
	if generateSampleSize != 4 {
		t.Fatalf("sample size=%d, contract says the first 4 codes", generateSampleSize)
	}
	gen := sourcetest.Load(t, ".").Decl("Service.GenerateCodes")
	for _, ordered := range []string{
		"INSERT INTO gift_card_batches", "INSERT INTO gift_card_codes", "loadBatchTx(", "audit.Write(",
	} {
		if !strings.Contains(gen, ordered) {
			t.Fatalf("GenerateCodes missing %q", ordered)
		}
	}
	if strings.Index(gen, "INSERT INTO gift_card_batches") > strings.Index(gen, "INSERT INTO gift_card_codes") {
		t.Fatal("the batch row must exist before codes reference it")
	}
	if !strings.Contains(gen, "len(sample) < generateSampleSize") {
		t.Fatal("GenerateCodes must cap the plaintext it returns")
	}
}

// 一次性导出：打标记、读明文、写审计在同一事务里，审计不记任何码。
func TestExportBatchMarksReadsAndAuditsInOneTransaction(t *testing.T) {
	export := sourcetest.Load(t, ".").Decl("Service.ExportBatch")
	if n := strings.Count(export, "s.pool.InTx("); n != 1 {
		t.Fatalf("ExportBatch opens %d transactions, want 1", n)
	}
	last := -1
	for _, step := range []string{
		"FOR UPDATE", "return ErrBatchAlreadyExported", "SET exported_at = now()",
		"exported_at IS NULL", "FROM gift_card_codes", "audit.Write(",
	} {
		at := strings.Index(export, step)
		if at <= last {
			t.Fatalf("ExportBatch step %q missing or out of order", step)
		}
		last = at
	}
	digest := export[strings.Index(export, "AfterDigest"):]
	digest = digest[:strings.Index(digest, "},")]
	if strings.Contains(digest, "Code") || strings.Contains(digest, "Rows[") {
		t.Fatalf("export audit digest must not carry codes: %s", digest)
	}
}
