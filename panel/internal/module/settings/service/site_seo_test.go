package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSiteDefaultsSEOFields(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	site, err := s.Site(context.Background())
	if err != nil {
		t.Fatalf("site: %v", err)
	}
	if site.SEOKeywords != "" || site.SEODescription != "" || site.AllowIndexing != false {
		t.Fatalf("defaults: %+v", site)
	}
}

func TestSetSiteSEOKeywordsTrimAndListener(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	ln := &countingListener{}
	s.UseSiteChangeListener(ln)
	ctx := context.Background()

	kw := "  加速, 节点  "
	if err := s.SetSite(ctx, SiteUpdate{SEOKeywords: &kw}); err != nil {
		t.Fatalf("set: %v", err)
	}
	site, err := s.Site(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if site.SEOKeywords != "加速, 节点" {
		t.Fatalf("keywords %q", site.SEOKeywords)
	}
	if site.Name != defaultSiteName || site.Timezone != defaultSiteTimezone {
		t.Fatalf("name/tz changed: %+v", site)
	}
	if len(al.entries) != 1 {
		t.Fatalf("audit count %d", len(al.entries))
	}
	if al.entries[0].action != auditSiteChanged {
		t.Fatalf("audit action %s", al.entries[0].action)
	}
	if ln.site != 1 {
		t.Fatalf("listener site %d", ln.site)
	}
}

func TestSetSiteSEOValidation(t *testing.T) {
	s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
	ctx := context.Background()

	longKW := strings.Repeat("字", SiteSEOKeywordsMax+1)
	err := s.SetSite(ctx, SiteUpdate{SEOKeywords: &longKW})
	assertFieldValidation(t, err, "seo_keywords")

	longDesc := strings.Repeat("描", SiteSEODescriptionMax+1)
	err = s.SetSite(ctx, SiteUpdate{SEODescription: &longDesc})
	assertFieldValidation(t, err, "seo_description")

	bad := "a\nb"
	err = s.SetSite(ctx, SiteUpdate{SEOKeywords: &bad})
	assertFieldValidation(t, err, "seo_keywords")
}

func TestSetSiteAllowIndexing(t *testing.T) {
	st := &kvStore{kv: map[string]json.RawMessage{}}
	s := newService(st, fakeTx{}, nil, nil, &auditLog{})
	ctx := context.Background()

	allow := true
	if err := s.SetSite(ctx, SiteUpdate{AllowIndexing: &allow}); err != nil {
		t.Fatalf("set: %v", err)
	}
	site, err := s.Site(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !site.AllowIndexing {
		t.Fatal("allow_indexing want true")
	}
}

func TestSiteURL(t *testing.T) {
	s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
	ctx := context.Background()

	cases := []struct {
		in, want string
	}{
		{"https://panel.example.test", "https://panel.example.test/"},
		{"https://panel.example.test/base/", "https://panel.example.test/base/"},
		{"", ""},
		{"ftp://x.example.test", ""},
		{"not a url", ""},
	}
	for _, tc := range cases {
		s.UsePublicBaseURL(tc.in)
		got := s.SiteURL(ctx)
		if got != tc.want {
			t.Fatalf("SiteURL(%q) = %q want %q", tc.in, got, tc.want)
		}
	}
}

func TestSiteSEOMaxConstants(t *testing.T) {
	if SiteSEOKeywordsMax != 200 || SiteSEODescriptionMax != 160 {
		t.Fatalf("max constants")
	}
	if utf8.RuneCountInString(strings.Repeat("x", SiteSEOKeywordsMax)) != SiteSEOKeywordsMax {
		t.Fatal("keyword max count")
	}
}
