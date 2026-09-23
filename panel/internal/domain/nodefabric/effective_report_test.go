package nodefabric

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestCanonicalJSONPreservesLargeIntegersAndRejectsDuplicateKeys(t *testing.T) {
	raw := []byte(`{"n":9007199254740993,"nested":{"x":9007199254740995}}`)
	canonical, err := canonicalJSON(raw)
	if err != nil {
		t.Fatalf("canonical JSON rejected: %v", err)
	}
	if !bytes.Contains(canonical, []byte("9007199254740993")) ||
		!bytes.Contains(canonical, []byte("9007199254740995")) {
		t.Fatalf("large integer precision was lost: %s", canonical)
	}
	if _, err := canonicalJSON([]byte(`{"x":1,"x":2}`)); err == nil {
		t.Fatal("duplicate JSON object key was accepted")
	}
}

func TestEffectiveReportRejectsZeroAndNoncanonicalIdentityBeforeDatabase(t *testing.T) {
	svc := &Service{}
	validHash := base64.StdEncoding.EncodeToString(make([]byte, 32))
	base := EffectiveConfigReportInput{
		ReportID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", ReleaseID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
		Generation: 1, ContentHash: validHash, Phase: "verified",
	}
	cases := map[string]EffectiveConfigReportInput{
		"zero_report": func() EffectiveConfigReportInput {
			v := base
			v.ReportID = "00000000-0000-0000-0000-000000000000"
			return v
		}(),
		"upper_release":   func() EffectiveConfigReportInput { v := base; v.ReleaseID = strings.ToUpper(v.ReleaseID); return v }(),
		"zero_generation": func() EffectiveConfigReportInput { v := base; v.Generation = 0; return v }(),
		"padded_hash":     func() EffectiveConfigReportInput { v := base; v.ContentHash += "="; return v }(),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if err := svc.ReportEffectiveConfigApplied(context.Background(),
				"cccccccc-cccc-4ccc-8ccc-cccccccccccc", "dddddddd-dddd-4ddd-8ddd-dddddddddddd", input); err == nil {
				t.Fatal("invalid effective report was accepted")
			}
		})
	}
}
