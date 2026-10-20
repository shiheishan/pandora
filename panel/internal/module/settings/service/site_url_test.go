package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/pkg/errs"
)

// siteConfigOK 是假面板的 /v1/site-config：与真 handler 同形状。
func siteConfigOK(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != siteURLProbePath {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"registration_mode":"open","email_verification":false,"password_reset":true}`))
}

// urlListener 记下被调用时 SiteURL 读到的值。
type urlListener struct {
	s    *Service
	seen []string
}

func (l *urlListener) SiteURLChanged(ctx context.Context) { l.seen = append(l.seen, l.s.SiteURL(ctx)) }

// failingStore 读设置一律失败。
type failingStore struct{ kvStore }

func (f *failingStore) Values(context.Context, []string) (map[string]json.RawMessage, error) {
	return nil, errors.New("db down")
}

func assertURLField(t *testing.T, err error, want string) {
	t.Helper()
	e := errs.As(err)
	if e == nil || e.Code != errs.ValidationFailed || e.Fields["url"] != want {
		t.Fatalf("want url field %q, got %v", want, err)
	}
}

func TestNormalizeSiteURL(t *testing.T) {
	accept := []struct {
		in        string
		allowHTTP bool
		want      string
	}{
		{"", false, ""},
		{"  ", false, ""},
		{"https://panel.example.test", false, "https://panel.example.test"},
		{"https://panel.example.test/", false, "https://panel.example.test"},
		{" HTTPS://Panel.Example.TEST./ ", false, "https://panel.example.test"},
		{"https://panel.example.test:443", false, "https://panel.example.test"},
		{"https://panel.example.test:8443/", false, "https://panel.example.test:8443"},
		{"http://panel.example.test:80", true, "http://panel.example.test"},
		{"http://127.0.0.1:9000", true, "http://127.0.0.1:9000"},
		{"https://[2001:db8::1]:8443", false, "https://[2001:db8::1]:8443"},
	}
	for _, tc := range accept {
		got, msg := normalizeSiteURL(tc.in, tc.allowHTTP)
		if msg != "" || got != tc.want {
			t.Errorf("normalizeSiteURL(%q, %v) = %q, %q; want %q", tc.in, tc.allowHTTP, got, msg, tc.want)
		}
	}

	reject := []struct {
		in        string
		allowHTTP bool
		msg       string
	}{
		{"http://panel.example.test", false, siteURLMsgHTTPS},
		{"panel.example.test", true, siteURLMsgFormat},
		{"ftp://panel.example.test", true, siteURLMsgFormat},
		{"https://panel.example.test/sub", false, siteURLMsgFormat},
		{"https://panel.example.test/sub/", false, siteURLMsgFormat},
		{"https://panel.example.test/?a=1", false, siteURLMsgFormat},
		{"https://panel.example.test?", false, siteURLMsgFormat},
		{"https://panel.example.test/#x", false, siteURLMsgFormat},
		{"https://user:pw@panel.example.test", false, siteURLMsgFormat},
		{"https://panel.example.test:0", false, siteURLMsgFormat},
		{"https://panel.example.test:70000", false, siteURLMsgFormat},
		{"https://pan el.example.test", false, siteURLMsgFormat},
		{"https://", false, siteURLMsgFormat},
		{"mailto:a@panel.example.test", true, siteURLMsgFormat},
		{"https://" + strings.Repeat("a", SiteURLMaxLen) + ".example.test", false, siteURLMsgFormat},
	}
	for _, tc := range reject {
		got, msg := normalizeSiteURL(tc.in, tc.allowHTTP)
		if msg != tc.msg || got != "" {
			t.Errorf("normalizeSiteURL(%q, %v) = %q, %q; want reject %q", tc.in, tc.allowHTTP, got, msg, tc.msg)
		}
	}
}

func TestSiteURLFallsBackToConfig(t *testing.T) {
	ctx := context.Background()

	t.Run("没存用配置", func(t *testing.T) {
		s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
		s.UseSiteURLConfig("https://panel.example.test", false)
		if got := s.SiteURL(ctx); got != "https://panel.example.test/" {
			t.Fatalf("SiteURL = %q", got)
		}
		if got := s.SiteURLFallback(); got != "https://panel.example.test" {
			t.Fatalf("SiteURLFallback = %q", got)
		}
	})

	t.Run("存了用存的", func(t *testing.T) {
		st := &kvStore{kv: map[string]json.RawMessage{keySiteURL: json.RawMessage(`"https://new.example.test"`)}}
		s := newService(st, fakeTx{}, nil, nil, &auditLog{})
		s.UseSiteURLConfig("https://panel.example.test", false)
		if got := s.SiteURL(ctx); got != "https://new.example.test/" {
			t.Fatalf("SiteURL = %q", got)
		}
	})

	t.Run("存的值坏了用配置", func(t *testing.T) {
		st := &kvStore{kv: map[string]json.RawMessage{keySiteURL: json.RawMessage(`"https://new.example.test/sub"`)}}
		s := newService(st, fakeTx{}, nil, nil, &auditLog{})
		s.UseSiteURLConfig("https://panel.example.test", false)
		if got := s.SiteURL(ctx); got != "https://panel.example.test/" {
			t.Fatalf("SiteURL = %q", got)
		}
	})

	t.Run("生产里存的 http 不用", func(t *testing.T) {
		st := &kvStore{kv: map[string]json.RawMessage{keySiteURL: json.RawMessage(`"http://new.example.test"`)}}
		s := newService(st, fakeTx{}, nil, nil, &auditLog{})
		s.UseSiteURLConfig("https://panel.example.test", false)
		if got := s.SiteURL(ctx); got != "https://panel.example.test/" {
			t.Fatalf("SiteURL = %q", got)
		}
	})

	t.Run("读库失败用配置", func(t *testing.T) {
		s := newService(&failingStore{}, fakeTx{}, nil, nil, &auditLog{})
		s.UseSiteURLConfig("https://panel.example.test", false)
		if got := s.SiteURL(ctx); got != "https://panel.example.test/" {
			t.Fatalf("SiteURL = %q", got)
		}
		if got := s.SiteURLAliases(ctx); !slices.Equal(got, []string{"https://panel.example.test/"}) {
			t.Fatalf("aliases = %v", got)
		}
	})
}

func TestSetSiteURLChangesTakeEffectImmediately(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(siteConfigOK))
	defer srv.Close()

	st := &kvStore{kv: map[string]json.RawMessage{}}
	al := &auditLog{}
	s := newService(st, fakeTx{}, nil, nil, al)
	s.UseSiteURLConfig("http://panel.example.test", true)
	ln := &urlListener{s: s}
	s.UseSiteURLChangeListener(ln)
	site := &countingListener{}
	s.UseSiteChangeListener(site)

	// 先读一次，让快照里存下旧值
	if got := s.SiteURL(ctx); got != "http://panel.example.test/" {
		t.Fatalf("before = %q", got)
	}

	newURL := srv.URL + "/"
	if err := s.SetSite(ctx, SiteUpdate{URL: &newURL}); err != nil {
		t.Fatalf("SetSite: %v", err)
	}
	want := srv.URL + "/"
	if got := s.SiteURL(ctx); got != want {
		t.Fatalf("after = %q want %q（快照应在提交后立即失效）", got, want)
	}
	if !slices.Equal(ln.seen, []string{want}) {
		t.Fatalf("listener saw %v, want [%s]", ln.seen, want)
	}
	if site.site != 1 {
		t.Fatalf("site listener = %d, want 1（入口页 canonical 要跟着变）", site.site)
	}
	if got := s.SiteURLAliases(ctx); !slices.Equal(got, []string{want, "http://panel.example.test/"}) {
		t.Fatalf("aliases = %v", got)
	}
	gotSite, err := s.Site(ctx)
	if err != nil || gotSite.URL != srv.URL {
		t.Fatalf("Site().URL = %q, %v", gotSite.URL, err)
	}
	if len(al.entries) != 1 || al.entries[0].action != auditSiteURLChanged {
		t.Fatalf("audit = %+v", al.entries)
	}
	d := al.entries[0].data
	if d["before"] != "" || d["after"] != srv.URL || d["effective_before"] != "http://panel.example.test" || d["effective_after"] != srv.URL {
		t.Fatalf("audit data = %v", d)
	}

	// 同一个值再存：不核对、不写、不通知
	srv.Close()
	if err := s.SetSite(ctx, SiteUpdate{URL: &newURL}); err != nil {
		t.Fatalf("same value: %v", err)
	}
	if len(ln.seen) != 1 || len(al.entries) != 1 {
		t.Fatalf("noop should not notify or audit: seen=%v audit=%d", ln.seen, len(al.entries))
	}

	// 恢复用配置：不核对（服务器已关也能存），上一个生效值记成刚才那个
	empty := ""
	if err := s.SetSite(ctx, SiteUpdate{URL: &empty}); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := s.SiteURL(ctx); got != "http://panel.example.test/" {
		t.Fatalf("after reset = %q", got)
	}
	if got := s.SiteURLAliases(ctx); !slices.Equal(got, []string{"http://panel.example.test/", want}) {
		t.Fatalf("aliases after reset = %v", got)
	}
}

func TestSetSiteURLProbe(t *testing.T) {
	ctx := context.Background()
	newSvc := func(dev bool) (*Service, *auditLog) {
		al := &auditLog{}
		s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, al)
		s.UseSiteURLConfig("https://panel.example.test", dev)
		return s, al
	}

	t.Run("开发环境收 http 且连通", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(siteConfigOK))
		defer srv.Close()
		s, _ := newSvc(true)
		u := srv.URL
		if err := s.SetSite(ctx, SiteUpdate{URL: &u}); err != nil {
			t.Fatalf("SetSite: %v", err)
		}
	})

	t.Run("生产不收 http", func(t *testing.T) {
		s, al := newSvc(false)
		u := "http://new.example.test"
		assertURLField(t, s.SetSite(ctx, SiteUpdate{URL: &u}), siteURLMsgHTTPS)
		if len(al.entries) != 0 {
			t.Fatal("rejected value must not be audited")
		}
	})

	t.Run("生产不许连回环", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(siteConfigOK))
		defer srv.Close()
		s, al := newSvc(false)
		u := srv.URL // https://127.0.0.1:端口
		assertURLField(t, s.SetSite(ctx, SiteUpdate{URL: &u}), siteURLMsgProbe)
		if len(al.entries) != 0 {
			t.Fatal("probe failure must not write")
		}
	})

	t.Run("生产 https 连通（换成信任测试证书的客户端）", func(t *testing.T) {
		srv := httptest.NewTLSServer(http.HandlerFunc(siteConfigOK))
		defer srv.Close()
		s, _ := newSvc(false)
		c := srv.Client()
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		s.su.probeClient = c
		u := srv.URL
		if err := s.SetSite(ctx, SiteUpdate{URL: &u}); err != nil {
			t.Fatalf("SetSite: %v", err)
		}
	})

	fails := map[string]http.HandlerFunc{
		"404": http.NotFound,
		"500": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
		"不跟随跳转": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == siteURLProbePath {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
				return
			}
			// 跳转目标回的是合格的 site-config，跟过去就会误判为通
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"registration_mode":"open"}`))
		},
		"不是 JSON": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write([]byte(`{"registration_mode":"open"}`))
		},
		"别的 JSON": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
		},
	}
	for name, h := range fails {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(h)
			defer srv.Close()
			s, al := newSvc(true)
			u := srv.URL
			assertURLField(t, s.SetSite(ctx, SiteUpdate{URL: &u}), siteURLMsgProbe)
			if len(al.entries) != 0 {
				t.Fatal("probe failure must not write")
			}
		})
	}

	t.Run("连不上", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(siteConfigOK))
		u := srv.URL
		srv.Close()
		s, _ := newSvc(true)
		assertURLField(t, s.SetSite(ctx, SiteUpdate{URL: &u}), siteURLMsgProbe)
	})
}

func TestMissingPortsListsSiteURLListener(t *testing.T) {
	s := newService(&kvStore{kv: map[string]json.RawMessage{}}, fakeTx{}, nil, nil, &auditLog{})
	if !slices.Contains(s.MissingPorts(), "SiteURLChangeListener") {
		t.Fatalf("MissingPorts %v", s.MissingPorts())
	}
	s.UseSiteURLChangeListener(&urlListener{s: s})
	if slices.Contains(s.MissingPorts(), "SiteURLChangeListener") {
		t.Fatalf("MissingPorts %v", s.MissingPorts())
	}
}
