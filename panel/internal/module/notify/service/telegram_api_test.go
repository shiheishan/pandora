package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTelegramSendMessageRequest(t *testing.T) {
	const token = "123:ABC"
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
			t.Fatalf("content-type %q", ct)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("method %q", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTelegramClient(srv.URL)
	if err := c.SendMessage(context.Background(), token, 42, "hello"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot"+token+"/sendMessage" {
		t.Fatalf("path %q", gotPath)
	}
	if gotBody["chat_id"] != float64(42) || gotBody["text"] != "hello" || gotBody["disable_web_page_preview"] != true {
		t.Fatalf("body %+v", gotBody)
	}
}

func TestTelegramSetWebhookRequest(t *testing.T) {
	const token = "9:xyz"
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTelegramClient(srv.URL)
	if err := c.SetWebhook(context.Background(), token, "https://panel.example/hook", "sec"); err != nil {
		t.Fatal(err)
	}
	if gotBody["url"] != "https://panel.example/hook" || gotBody["secret_token"] != "sec" {
		t.Fatalf("body %+v", gotBody)
	}
	au, ok := gotBody["allowed_updates"].([]interface{})
	if !ok || len(au) != 1 || au[0] != "message" {
		t.Fatalf("allowed_updates %+v", gotBody["allowed_updates"])
	}
}

func TestTelegramAPIErrors(t *testing.T) {
	const token = "1:tok"
	cases := []struct {
		status int
		body   string
		perm   bool
	}{
		{403, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`, true},
		{500, `{"ok":false,"error_code":500,"description":"internal"}`, false},
		{200, `{"ok":false,"error_code":400,"description":"bad"}`, false},
	}
	for _, tc := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		defer srv.Close()
		c := newTelegramClient(srv.URL)
		err := c.SendMessage(context.Background(), token, 1, "x")
		if err == nil {
			t.Fatalf("status %d: expected error", tc.status)
		}
		te, ok := err.(*TelegramError)
		if !ok {
			t.Fatalf("%T %v", err, err)
		}
		if te.Permanent != tc.perm {
			t.Fatalf("status %d permanent %v want %v", tc.status, te.Permanent, tc.perm)
		}
		if tc.status == 403 && !strings.Contains(te.Detail, "Forbidden") {
			t.Fatalf("detail %q", te.Detail)
		}
	}
}

func TestTelegramConnFailureHidesToken(t *testing.T) {
	const token = "SECRET_TOKEN_99"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	url := srv.URL
	srv.Close()

	c := newTelegramClient(url)
	err := c.SendMessage(context.Background(), token, 1, "hi")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) {
		t.Fatalf("error leaks token: %v", err)
	}
}

func TestTelegramTruncateText(t *testing.T) {
	const token = "t:1"
	var textLen int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		textLen = len([]rune(body["text"].(string)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	long := strings.Repeat("字", 5000)
	c := newTelegramClient(srv.URL)
	if err := c.SendMessage(context.Background(), token, 1, long); err != nil {
		t.Fatal(err)
	}
	if textLen != 4096 {
		t.Fatalf("rune len %d", textLen)
	}
}
