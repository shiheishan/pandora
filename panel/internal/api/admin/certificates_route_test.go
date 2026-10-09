package admin

import (
	"io"
	"log/slog"
	"testing"

	"github.com/go-chi/chi/v5"
)

// 没有信封加密器时证书路由不挂（不留到请求时才对 nil 加解密器 panic）
func TestCertificateRoutesNeedEnvelope(t *testing.T) {
	r := chi.NewRouter()
	registerCertificateRoutes(r, Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	if n := len(r.Routes()); n != 0 {
		t.Fatalf("certificate routes mounted without an envelope: %d", n)
	}
	if newCertHandlers(Deps{}) != nil {
		t.Fatal("handlers must not be built without an envelope")
	}
}
