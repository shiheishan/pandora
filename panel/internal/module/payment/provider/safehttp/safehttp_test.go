package safehttp

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckDialAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:80", "100.100.100.200:80", "100.64.0.1:443", "0.0.0.0:443",
		"0.1.2.3:443", "224.0.0.1:443", "255.255.255.255:443", "198.18.0.1:443",
		"[::1]:443", "[fc00::1]:443", "[fd00:ec2::254]:80", "[fe80::1]:443", "[::]:443",
		"[::ffff:127.0.0.1]:443", "[64:ff9b::a00:1]:443", "[ff02::1]:443",
	}
	for _, a := range blocked {
		if err := CheckDialAddr(a); err == nil {
			t.Errorf("%s 应当被拒绝", a)
		}
	}
	for _, a := range []string{"203.0.113.10:443", "[2001:db8::1]:443", "8.8.8.8:443"} {
		if err := CheckDialAddr(a); err != nil {
			t.Errorf("%s 不应被拒绝: %v", a, err)
		}
	}
}

func TestNewClientDoesNotFollowRedirect(t *testing.T) {
	targetHit := false
	mux := http.NewServeMux()
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusOK)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := NewClient(true)
	resp, err := client.Get(srv.URL + "/redirect")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		t.Errorf("状态码 = %d, 期望 %d (302)", resp.StatusCode, http.StatusFound)
	}
	if targetHit {
		t.Error("不应跟随跳转到目标路径")
	}
}

func TestNewClientRefusesLoopbackWhenNotAllowed(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := NewClient(false)
	resp, err := client.Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("未允许访问私网时，请求回环地址应当报错")
	}
	if hit {
		t.Fatal("请求不应到达回环服务")
	}
}

func TestReadBodyLimit(t *testing.T) {
	exact := bytes.Repeat([]byte("a"), MaxResponseBytes)
	data, err := ReadBody(bytes.NewReader(exact))
	if err != nil {
		t.Fatalf("读取正好 %d 字节失败: %v", MaxResponseBytes, err)
	}
	if len(data) != MaxResponseBytes {
		t.Errorf("读取长度 = %d, 期望 %d", len(data), MaxResponseBytes)
	}

	over := bytes.Repeat([]byte("b"), MaxResponseBytes+1)
	_, err = ReadBody(bytes.NewReader(over))
	if err == nil {
		t.Fatal("超过 MaxResponseBytes 时应当返回错误")
	}
	if !strings.Contains(err.Error(), "响应超过 256 KiB") {
		t.Errorf("错误信息不符: %v", err)
	}
}
