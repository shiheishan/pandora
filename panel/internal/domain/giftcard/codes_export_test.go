// [INPUT]: 依赖 codes_export.go 的 maskedCodeSQL、codesExportSelect、CodeReportRow，依赖 codes.go 的 codeFilterCond、CodeFilter.check 与 batches.go 的 MaskCode / maskedTail
// [OUTPUT]: 对外提供 TestCodesExportSQLReadsOnlyTheMask、TestMaskedCodeSQLMirrorsMaskCode、TestCodeReportRowHasNoPlaintextField、TestExportCodesRejectsBadFilterBeforeTouchingTheDatabase
// [POS]: giftcard 掩码报表导出的单元守卫：导出 SQL 除掩码表达式外不碰 code 列、SQL 掩码与 MaskCode 同形、报表行没有放明文的字段；逐行比对在 codes_export_pg18_test.go
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package giftcard

import (
	"context"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 明文列只能以掩码表达式的形式出现在导出 SQL 里：拿掉那一段，剩下的不得再提到 code 列。
func TestCodesExportSQLReadsOnlyTheMask(t *testing.T) {
	sql := codesExportSelect + codeFilterCond
	if strings.Count(sql, maskedCodeSQL) != 1 {
		t.Fatalf("export SQL must select the masked expression exactly once:\n%s", sql)
	}
	rest := strings.Replace(sql, maskedCodeSQL, "", 1)
	if regexp.MustCompile(`\bcode\b`).MatchString(rest) {
		t.Fatalf("export SQL reads the plaintext code column outside the mask:\n%s", rest)
	}
}

// maskedCodeSQL 按 MaskCode 的规则写成 SQL：去掉末尾 maskedTail 位，换成同样多个 •。
// 这里钉住表达式里的位数，并用同一语义在 Go 里复算 8–32 位（表上 CHECK 的范围）。
func TestMaskedCodeSQLMirrorsMaskCode(t *testing.T) {
	tail := strconv.Itoa(maskedTail)
	if maskedCodeSQL != "left(c.code, -"+tail+") || repeat('•', least(char_length(c.code), "+tail+"))" {
		t.Fatalf("maskedCodeSQL drifted from maskedTail=%d: %s", maskedTail, maskedCodeSQL)
	}
	sqlMask := func(code string) string { // left(s, -n) || repeat('•', least(len, n))
		r := []rune(code)
		keep := max(len(r)-maskedTail, 0)
		return string(r[:keep]) + strings.Repeat("•", min(len(r), maskedTail))
	}
	alphabet := "ABCDEFGHJKMNPQRSTUVWXYZ23456789ABCDEFG"
	for n := 8; n <= 32; n++ {
		code := alphabet[:n]
		if got, want := sqlMask(code), MaskCode(code); got != want {
			t.Fatalf("len %d: sql mask %q != MaskCode %q", n, got, want)
		}
	}
}

func TestCodeReportRowHasNoPlaintextField(t *testing.T) {
	typ := reflect.TypeFor[CodeReportRow]()
	for i := range typ.NumField() {
		if name := typ.Field(i).Name; name == "Code" || name == "Plain" || name == "Plaintext" {
			t.Fatalf("CodeReportRow must not carry a plaintext field, found %s", name)
		}
	}
}

// 筛选值非法时在事务之前就拒绝（Service 没有连接池也不会 panic），与列表同一套校验。
func TestExportCodesRejectsBadFilterBeforeTouchingTheDatabase(t *testing.T) {
	s := &Service{}
	for name, f := range map[string]CodeFilter{
		"未知状态":     {Status: "used%"},
		"模板非 UUID": {TemplateID: "x"},
		"批次非 UUID": {BatchID: "1; DROP"},
	} {
		if _, err := s.ExportCodes(context.Background(), "t", "a", f); err == nil {
			t.Errorf("%s: want 400, got nil", name)
		}
	}
}
