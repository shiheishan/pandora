package node

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aegispanel/aegis/internal/platform/httpx"
)

// goldenServerRequest 是金样本 server_requests 里的一条向量（字段见合约 §17）。
type goldenServerRequest struct {
	Name        string `json:"name"`
	Valid       bool   `json:"valid"`
	PreimageHex string `json:"preimage_hex"`
	Signature   string `json:"signature"`
	Method      string `json:"method"`
	Target      string `json:"target"`
	TenantID    string `json:"tenant_id"`
	ServerID    string `json:"server_id"`
	Serial      int64  `json:"serial"`
	Timestamp   string `json:"ts"`
	Nonce       string `json:"nonce"`
	ReportID    string `json:"report_id"`
	Body        string `json:"body"`
}

func loadServerRequestGolden(t *testing.T) (ed25519.PublicKey, []goldenServerRequest) {
	t.Helper()
	_, thisFile, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "platform", "bindingcontract", "testdata",
		"bindingcontract-v1-golden.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Keys struct {
			ServerPublicKey string `json:"server_public_key"`
		} `json:"keys"`
		ServerRequests []goldenServerRequest `json:"server_requests"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(doc.Keys.ServerPublicKey)
	if err != nil || len(pub) != ed25519.PublicKeySize || len(doc.ServerRequests) < 2 {
		t.Fatalf("golden keys or vectors missing: %v", err)
	}
	return ed25519.PublicKey(pub), doc.ServerRequests
}

// TestServerRequestGoldenVectorsThroughHTTP 把金样本的每条服务器请求向量还原成一个真实的
// HTTP 请求（方法、目标、头、请求体、租户），交给网关的解析函数：合法向量重建出的原像必须与
// 合约原像逐字节相同、能用金样本公钥验过签名；非法向量必须被拒。
func TestServerRequestGoldenVectorsThroughHTTP(t *testing.T) {
	pub, vectors := loadServerRequestGolden(t)
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	valid := 0
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			sig := v.Signature
			if sig == "" {
				sig = base64.StdEncoding.EncodeToString(make([]byte, ed25519.SignatureSize))
			}
			r := httptest.NewRequest(v.Method, "http://node.example"+v.Target, strings.NewReader(v.Body))
			r.Header.Set("X-Server-Id", v.ServerID)
			r.Header.Set("X-Server-Serial", strconv.FormatInt(v.Serial, 10))
			r.Header.Set("X-Server-Ts", v.Timestamp)
			r.Header.Set("X-Server-Nonce", v.Nonce)
			r.Header.Set("X-Server-Sig", sig)
			if v.ReportID != "" {
				r.Header.Set("X-Report-Id", v.ReportID)
			}
			r = r.WithContext(httpx.WithTenantID(r.Context(), v.TenantID))
			req, reason, err := parseServerSignedRequest(r, now)
			if !v.Valid {
				if err == nil {
					t.Fatalf("invalid vector %q was accepted", v.Name)
				}
				return
			}
			if err != nil {
				t.Fatalf("valid vector rejected: %s: %v", reason, err)
			}
			want, _ := hex.DecodeString(v.PreimageHex)
			if string(req.preimage) != string(want) {
				t.Fatalf("preimage differs from the contract golden\n got %q\nwant %q", req.preimage, want)
			}
			if !ed25519.Verify(pub, req.preimage, req.sig) {
				t.Fatal("golden signature does not verify over the rebuilt preimage")
			}
			// 租户写进原像：换一个租户解析，签名必须验不过
			other := r.Clone(httpx.WithTenantID(r.Context(), "0193f0b0-1111-7000-8000-0000000000a2"))
			other.Body = r.Body
			if alt, _, err := parseServerSignedRequest(other, now); err == nil && ed25519.Verify(pub, alt.preimage, alt.sig) {
				t.Fatal("signature still verifies under another tenant")
			}
			valid++
		})
	}
	if valid < 2 {
		t.Fatalf("expected at least two valid golden vectors, got %d", valid)
	}
}

// 签名头的格式与时间窗：缺头、非规范 serial、窗外时间戳、坏 nonce、坏签名编码都拒。
func TestParseServerSignedRequestRejectsMalformedHeaders(t *testing.T) {
	_, vectors := loadServerRequestGolden(t)
	base := vectors[0]
	now := time.Date(2026, 10, 7, 8, 0, 0, 0, time.UTC)
	cases := map[string]func(h map[string]string){
		"missing sig":     func(h map[string]string) { delete(h, "X-Server-Sig") },
		"serial zero":     func(h map[string]string) { h["X-Server-Serial"] = "0" },
		"serial padded":   func(h map[string]string) { h["X-Server-Serial"] = "01" },
		"serial too big":  func(h map[string]string) { h["X-Server-Serial"] = "2147483648" },
		"stale ts":        func(h map[string]string) { h["X-Server-Ts"] = "2026-10-07T07:54:59Z" },
		"future ts":       func(h map[string]string) { h["X-Server-Ts"] = "2026-10-07T08:05:01Z" },
		"short nonce":     func(h map[string]string) { h["X-Server-Nonce"] = "EREREREREREREREREREQ" },
		"bad sig":         func(h map[string]string) { h["X-Server-Sig"] = "not-base64" },
		"bad report id":   func(h map[string]string) { h["X-Report-Id"] = "0193F0B0-6666-7000-8000-0000000000F1" },
		"short signature": func(h map[string]string) { h["X-Server-Sig"] = base64.StdEncoding.EncodeToString([]byte("x")) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			h := map[string]string{
				"X-Server-Id": base.ServerID, "X-Server-Serial": "1", "X-Server-Ts": base.Timestamp,
				"X-Server-Nonce": base.Nonce, "X-Server-Sig": base.Signature, "X-Report-Id": base.ReportID,
			}
			mutate(h)
			r := httptest.NewRequest(base.Method, "http://node.example"+base.Target, strings.NewReader(base.Body))
			for k, v := range h {
				r.Header.Set(k, v)
			}
			r = r.WithContext(httpx.WithTenantID(r.Context(), base.TenantID))
			if _, _, err := parseServerSignedRequest(r, now); err == nil {
				t.Fatal("malformed request was accepted")
			}
		})
	}
}

// 服务器接口挂在 /v1 下：begin 不签名，其余三个都过签名中间件。
func TestServerEnrollmentRoutesRequireSignature(t *testing.T) {
	src, err := os.ReadFile("server_router.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	for _, want := range []string{
		`r.Post("/servers/enrollments", h.beginServerEnrollment)`,
		`r.Use(h.requireServerEnrollmentSignature)`,
		`r.Get("/status", h.serverEnrollmentStatus)`,
		`r.Post("/commit", h.commitServerEnrollment)`,
		`r.Post("/abort", h.abortServerEnrollment)`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("server_router.go is missing %s", want)
		}
	}
	router, err := os.ReadFile("router.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(router), "registerServerRoutes(r, h)") {
		t.Error("router.go does not mount the server routes")
	}
}
