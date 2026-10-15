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
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer srv.Close()

	c := newTelegramClient(srv.URL)
	mid, err := c.SendMessage(context.Background(), token, 42, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if mid != 77 {
		t.Fatalf("message_id %d", mid)
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
	updates := []string{"message", "chat_join_request"}
	if err := c.SetWebhook(context.Background(), token, "https://panel.example/hook", "sec", updates, true); err != nil {
		t.Fatal(err)
	}
	if gotBody["url"] != "https://panel.example/hook" || gotBody["secret_token"] != "sec" || gotBody["drop_pending_updates"] != true {
		t.Fatalf("body %+v", gotBody)
	}
	au, ok := gotBody["allowed_updates"].([]interface{})
	if !ok || len(au) != 2 || au[0] != "message" || au[1] != "chat_join_request" {
		t.Fatalf("allowed_updates %+v", gotBody["allowed_updates"])
	}

	// nil 切片默认 message
	gotBody = nil
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv2.Close()
	c2 := newTelegramClient(srv2.URL)
	if err := c2.SetWebhook(context.Background(), token, "https://panel.example/hook", "sec", nil, false); err != nil {
		t.Fatal(err)
	}
	au, ok = gotBody["allowed_updates"].([]interface{})
	if !ok || len(au) != 1 || au[0] != "message" {
		t.Fatalf("default allowed_updates %+v", gotBody["allowed_updates"])
	}
	if gotBody["drop_pending_updates"] != false {
		t.Fatalf("drop_pending_updates %+v", gotBody["drop_pending_updates"])
	}
}

func TestTelegramSetMyCommandsRequest(t *testing.T) {
	const token = "1:cmd"
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	cmds := []botCommand{{Command: "start", Description: "开始使用"}}
	c := newTelegramClient(srv.URL)
	if err := c.SetMyCommands(context.Background(), token, cmds); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/bot"+token+"/setMyCommands" {
		t.Fatalf("path %q", gotPath)
	}
	scope, ok := gotBody["scope"].(map[string]interface{})
	if !ok || scope["type"] != "all_private_chats" {
		t.Fatalf("scope %+v", gotBody["scope"])
	}
	list, ok := gotBody["commands"].([]interface{})
	if !ok || len(list) != 1 {
		t.Fatalf("commands %+v", gotBody["commands"])
	}
}

func TestTelegramApproveDeclineJoinRequest(t *testing.T) {
	const token = "1:join"
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		var b map[string]any
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	c := newTelegramClient(srv.URL)
	if err := c.ApproveChatJoinRequest(context.Background(), token, -100, 55); err != nil {
		t.Fatal(err)
	}
	if err := c.DeclineChatJoinRequest(context.Background(), token, -100, 56); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 {
		t.Fatalf("paths %v", paths)
	}
	if !strings.HasSuffix(paths[0], "approveChatJoinRequest") || !strings.HasSuffix(paths[1], "declineChatJoinRequest") {
		t.Fatalf("paths %v", paths)
	}
	if bodies[0]["chat_id"] != float64(-100) || bodies[0]["user_id"] != float64(55) {
		t.Fatalf("approve body %+v", bodies[0])
	}
	if bodies[1]["user_id"] != float64(56) {
		t.Fatalf("decline body %+v", bodies[1])
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
		_, err := c.SendMessage(context.Background(), token, 1, "x")
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
	_, err := c.SendMessage(context.Background(), token, 1, "hi")
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
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	defer srv.Close()

	long := strings.Repeat("字", 5000)
	c := newTelegramClient(srv.URL)
	if _, err := c.SendMessage(context.Background(), token, 1, long); err != nil {
		t.Fatal(err)
	}
	if textLen != 4096 {
		t.Fatalf("rune len %d", textLen)
	}
}

// sendMessage 成功时 result 带回整条消息；中文按 \uXXXX 转义后远超 8 KiB，仍要读出 message_id。
func TestTelegramSendMessageLargeEscapedResponse(t *testing.T) {
	const token = "t:big"
	text := strings.Repeat("\\u5de5\\u5355", 1500) // 3000 个汉字的转义写法，约 18 KB
	resp := `{"ok":true,"result":{"message_id":7,"chat":{"id":-100,"type":"supergroup"},"text":"` + text + `"}}`
	if len(resp) <= 8<<10 {
		t.Fatalf("夹具不够长：%d", len(resp))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, resp)
	}))
	defer srv.Close()

	c := newTelegramClient(srv.URL)
	mid, err := c.SendMessage(context.Background(), token, -100, "x")
	if err != nil {
		t.Fatal(err)
	}
	if mid != 7 {
		t.Fatalf("message_id %d", mid)
	}
}

// 2xx 但回包解析不了、或缺 message_id：当永久失败返回，不吞掉、也不触发重发。
func TestTelegramUnparsableSuccessIsPermanentError(t *testing.T) {
	const token = "t:bad"
	for _, body := range []string{`{"ok":true,"result":{"message_id":`, ``, `{"ok":true}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, body)
		}))
		c := newTelegramClient(srv.URL)
		_, err := c.SendMessage(context.Background(), token, 1, "x")
		srv.Close()
		te, ok := err.(*TelegramError)
		if !ok {
			t.Fatalf("body %q: err %T %v", body, err, err)
		}
		if !te.Permanent {
			t.Fatalf("body %q: want permanent", body)
		}
	}
}
