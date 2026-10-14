package render

// 与 golden 逐字节比对；带 -update 时用当前实现重写期望输出与 manifest 里的期望值（见 testdata_test.go）。

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
	for i := range m.Renders {
		r := &m.Renders[i]
		t.Run(r.Name, func(t *testing.T) {
			var nodes []Node
			if !r.Empty {
				nodes = loadNodes(t)
			}
			body, ct, count := Render(Format(r.Format), nodes, m.ProxyUUID, r.UA, r.Singbox.options())
			if *update {
				writeTestdata(t, "golden/"+r.File, body)
				r.ContentType = ct
				r.Count = count
				return
			}
			want := readTestdata(t, "golden/"+r.File)
			if !bytes.Equal(body, want) {
				t.Errorf("%s 与 golden 不同：%s", r.File, firstDiff(body, want))
			}
			if ct != r.ContentType || count != r.Count {
				t.Errorf("content-type=%q count=%d，golden %q %d", ct, count, r.ContentType, r.Count)
			}
		})
	}
	if *update {
		saveManifest(t, m)
	}
}

func TestGoldenExpired(t *testing.T) {
	m := loadManifest(t)
	if len(m.Expired) == 0 {
		t.Fatalf("manifest 里没有 expired：%s", regenerateHint)
	}
	for i := range m.Expired {
		e := &m.Expired[i]
		t.Run(e.Name, func(t *testing.T) {
			end, err := time.Parse(time.RFC3339, e.PeriodEnd)
			if err != nil {
				t.Fatal(err)
			}
			var loc *time.Location
			if !e.NilLocation {
				loc = time.FixedZone(e.TZName, e.TZOffsetSeconds)
			}
			notice := ExpiredNotice(end, loc)
			if *update {
				e.Notice = notice
			} else if notice != e.Notice {
				t.Errorf("ExpiredNotice = %q，golden %q", notice, e.Notice)
			}
			body, ct := RenderExpired(Format(e.Format), notice)
			if *update {
				writeTestdata(t, "golden/"+e.File, body)
				e.ContentType = ct
				return
			}
			want := readTestdata(t, "golden/"+e.File)
			if !bytes.Equal(body, want) {
				t.Errorf("%s 与 golden 不同：%s", e.File, firstDiff(body, want))
			}
			if ct != e.ContentType {
				t.Errorf("content-type=%q，golden %q", ct, e.ContentType)
			}
		})
	}
	if *update {
		saveManifest(t, m)
	}
}

func TestGoldenProfiles(t *testing.T) {
	m := loadManifest(t)
	if len(m.Profiles) == 0 {
		t.Fatalf("manifest 里没有 profiles：%s", regenerateHint)
	}
	for i := range m.Profiles {
		p := &m.Profiles[i]
		if p.ProfileName == "" {
			// 只记了 ContentDisposition(site)
			cd := ContentDisposition(p.Site)
			if *update {
				p.ContentDisposition = cd
				continue
			}
			if cd != p.ContentDisposition {
				t.Errorf("ContentDisposition(%q) = %q，golden %q", p.Site, cd, p.ContentDisposition)
			}
			continue
		}
		name := ProfileName(p.Site, p.Label, p.Plan)
		cd := ContentDisposition(name)
		if *update {
			p.ProfileName = name
			p.ContentDisposition = cd
			continue
		}
		if name != p.ProfileName {
			t.Errorf("ProfileName(%q, %q, %q) = %q，golden %q", p.Site, p.Label, p.Plan, name, p.ProfileName)
		}
		if cd != p.ContentDisposition {
			t.Errorf("ContentDisposition(%q) = %q，golden %q", name, cd, p.ContentDisposition)
		}
	}
	if *update {
		saveManifest(t, m)
	}
}

func TestGoldenFormats(t *testing.T) {
	m := loadManifest(t)
	if len(m.Formats) == 0 {
		t.Fatalf("manifest 里没有 formats：%s", regenerateHint)
	}
	for i := range m.Formats {
		f := &m.Formats[i]
		gotFormat := string(DetectFormat(f.UA, f.Explicit))
		gotFamily := UAFamily(f.UA)
		if *update {
			f.Format = gotFormat
			f.Family = gotFamily
			continue
		}
		if gotFormat != f.Format {
			t.Errorf("DetectFormat(%q, %q) = %s，golden %s", f.UA, f.Explicit, gotFormat, f.Format)
		}
		if gotFamily != f.Family {
			t.Errorf("UAFamily(%q) = %s，golden %s", f.UA, gotFamily, f.Family)
		}
	}
	if *update {
		saveManifest(t, m)
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
