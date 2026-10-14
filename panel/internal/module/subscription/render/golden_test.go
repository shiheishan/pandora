package render

// 与旧实现逐字节比对。golden 由 tools/subgolden 用旧代码生成（见 testdata_test.go），
// 这里把同样的输入喂给新包：任何一个字节不同都是移植走样。

import (
	"bytes"
	"fmt"
	"testing"
	"time"
)

func TestGoldenRender(t *testing.T) {
	m := loadManifest(t)
	if len(m.Renders) == 0 {
		t.Fatalf("manifest 里没有 renders：%s", regenerateHint)
	}
	for _, r := range m.Renders {
		t.Run(r.Name, func(t *testing.T) {
			var nodes []Node
			if !r.Empty {
				nodes = loadNodes(t)
			}
			body, ct, count := Render(Format(r.Format), nodes, m.ProxyUUID, r.UA, r.Singbox.options())
			want := readTestdata(t, "golden/"+r.File)
			if !bytes.Equal(body, want) {
				t.Errorf("%s 与旧实现不同：%s", r.File, firstDiff(body, want))
			}
			if ct != r.ContentType || count != r.Count {
				t.Errorf("content-type=%q count=%d，旧实现 %q %d", ct, count, r.ContentType, r.Count)
			}
		})
	}
}

func TestGoldenExpired(t *testing.T) {
	m := loadManifest(t)
	if len(m.Expired) == 0 {
		t.Fatalf("manifest 里没有 expired：%s", regenerateHint)
	}
	for _, e := range m.Expired {
		t.Run(e.Name, func(t *testing.T) {
			end, err := time.Parse(time.RFC3339, e.PeriodEnd)
			if err != nil {
				t.Fatal(err)
			}
			var loc *time.Location
			if !e.NilLocation {
				loc = time.FixedZone(e.TZName, e.TZOffsetSeconds)
			}
			if got := ExpiredNotice(end, loc); got != e.Notice {
				t.Errorf("ExpiredNotice = %q，旧实现 %q", got, e.Notice)
			}
			body, ct := RenderExpired(Format(e.Format), e.Notice)
			want := readTestdata(t, "golden/"+e.File)
			if !bytes.Equal(body, want) {
				t.Errorf("%s 与旧实现不同：%s", e.File, firstDiff(body, want))
			}
			if ct != e.ContentType {
				t.Errorf("content-type=%q，旧实现 %q", ct, e.ContentType)
			}
		})
	}
}

func TestGoldenProfiles(t *testing.T) {
	m := loadManifest(t)
	if len(m.Profiles) == 0 {
		t.Fatalf("manifest 里没有 profiles：%s", regenerateHint)
	}
	for _, p := range m.Profiles {
		if p.ProfileName == "" {
			// 只记了 ContentDisposition(site)
			if got := ContentDisposition(p.Site); got != p.ContentDisposition {
				t.Errorf("ContentDisposition(%q) = %q，旧实现 %q", p.Site, got, p.ContentDisposition)
			}
			continue
		}
		name := ProfileName(p.Site, p.Label, p.Plan)
		if name != p.ProfileName {
			t.Errorf("ProfileName(%q, %q, %q) = %q，旧实现 %q", p.Site, p.Label, p.Plan, name, p.ProfileName)
		}
		if got := ContentDisposition(name); got != p.ContentDisposition {
			t.Errorf("ContentDisposition(%q) = %q，旧实现 %q", name, got, p.ContentDisposition)
		}
	}
}

func TestGoldenFormats(t *testing.T) {
	m := loadManifest(t)
	if len(m.Formats) == 0 {
		t.Fatalf("manifest 里没有 formats：%s", regenerateHint)
	}
	for _, f := range m.Formats {
		if got := DetectFormat(f.UA, f.Explicit); string(got) != f.Format {
			t.Errorf("DetectFormat(%q, %q) = %s，旧实现 %s", f.UA, f.Explicit, got, f.Format)
		}
		if got := UAFamily(f.UA); got != f.Family {
			t.Errorf("UAFamily(%q) = %s，旧实现 %s", f.UA, got, f.Family)
		}
	}
}

// firstDiff 给出第一个不同字节的偏移和前后各 80 字节的上下文，不打印整份。
func firstDiff(got, want []byte) string {
	i := 0
	for i < len(got) && i < len(want) && got[i] == want[i] {
		i++
	}
	clip := func(b []byte) string {
		lo, hi := max(i-80, 0), min(i+80, len(b))
		if lo > len(b) {
			return ""
		}
		return string(b[lo:hi])
	}
	return fmt.Sprintf("长度 %d / %d，第 %d 字节起不同\n  新：%q\n  旧：%q", len(got), len(want), i, clip(got), clip(want))
}
