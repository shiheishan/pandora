package epay

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/aegispanel/aegis/internal/module/payment/provider"
	"github.com/aegispanel/aegis/internal/pkg/money"
	"github.com/shopspring/decimal"
)

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestSignPayloadOrdering(t *testing.T) {
	params := map[string]string{
		"type":         "alipay",
		"pid":          "1001",
		"out_trade_no": "AO20260725-abc",
		"money":        "9.90",
		"name":         "标准套餐",
		"notify_url":   "https://a.example.com/notify",
		"return_url":   "https://a.example.com/return",
	}

	want := "money=9.90&name=标准套餐&notify_url=https://a.example.com/notify" +
		"&out_trade_no=AO20260725-abc&pid=1001&return_url=https://a.example.com/return" +
		"&type=alipay" + "SECRETKEY"

	if got := signPayload(params, "SECRETKEY"); got != want {
		t.Errorf("签名串不符\n got: %s\nwant: %s", got, want)
	}
}

func TestSignPayloadDoesNotURLEncode(t *testing.T) {
	params := map[string]string{
		"name":       "标准套餐",
		"notify_url": "https://a.example.com/notify?x=1",
	}
	got := signPayload(params, "K")

	if strings.Contains(got, "%") {
		t.Errorf("签名串被 URL 编码了，易支付要求原样拼接: %s", got)
	}
	if !strings.Contains(got, "标准套餐") {
		t.Errorf("中文商品名被改写: %s", got)
	}
	if !strings.Contains(got, "notify?x=1") {
		t.Errorf("URL 中的 ? 与 = 被编码: %s", got)
	}
}

func TestSignPayloadDropsEmptyAndSignFields(t *testing.T) {
	params := map[string]string{
		"pid":        "1001",
		"money":      "1.00",
		"return_url": "",
		"sign":       "deadbeef",
		"sign_type":  "MD5",
	}
	want := "money=1.00&pid=1001" + "K"
	if got := signPayload(params, "K"); got != want {
		t.Errorf("空值或 sign 字段未被剔除\n got: %s\nwant: %s", got, want)
	}
}

func TestSignPayloadAppendsKeyDirectly(t *testing.T) {
	got := signPayload(map[string]string{"a": "1"}, "MYKEY")
	if got != "a=1MYKEY" {
		t.Errorf("密钥拼接方式错误: %q，期望 %q", got, "a=1MYKEY")
	}
	if strings.Contains(got, "key=") {
		t.Errorf("密钥不应以 key= 形式拼接: %q", got)
	}
}

func TestSignMatchesMD5(t *testing.T) {
	params := map[string]string{"a": "1", "b": "2"}
	want := md5hex("a=1&b=2K")
	if got := Sign(params, "K"); got != want {
		t.Errorf("Sign = %s, 期望 %s", got, want)
	}
}

func TestVerifySign(t *testing.T) {
	params := map[string]string{
		"pid": "1001", "out_trade_no": "X1", "trade_no": "T1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "K")
	params["sign_type"] = "MD5"

	if !VerifySign(params, "K") {
		t.Error("正确签名未通过校验")
	}

	upper := map[string]string{}
	for k, v := range params {
		upper[k] = v
	}
	upper["sign"] = strings.ToUpper(params["sign"])
	if !VerifySign(upper, "K") {
		t.Error("大写签名未通过校验")
	}

	tampered := map[string]string{}
	for k, v := range params {
		tampered[k] = v
	}
	tampered["money"] = "0.01"
	if VerifySign(tampered, "K") {
		t.Error("金额被篡改后签名仍通过，防篡改失效")
	}

	if VerifySign(params, "WRONG") {
		t.Error("错误密钥下签名仍通过")
	}

	noSign := map[string]string{"pid": "1001"}
	if VerifySign(noSign, "K") {
		t.Error("无签名参数通过了校验")
	}
}

func testProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{
		BaseURL:          "https://pay.example.com",
		MerchantID:       "1001",
		Key:              "TESTKEY",
		AllowPrivateHost: true,
	})
	if err != nil {
		t.Fatalf("构造 Provider 失败: %v", err)
	}
	return p
}

func notifyInboundGET(params map[string]string) provider.Inbound {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return provider.Inbound{Method: http.MethodGet, Query: q}
}

func TestParseNotificationSuccess(t *testing.T) {
	p := testProvider(t)

	params := map[string]string{
		"pid":          "1001",
		"trade_no":     "20260725ABC",
		"out_trade_no": "AO20260725-xyz",
		"type":         "alipay",
		"name":         "标准套餐",
		"money":        "9.90",
		"trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")
	params["sign_type"] = "MD5"

	n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if !n.SignatureVerified {
		t.Error("正确签名的通知未通过验签")
	}
	if n.Status != provider.StatusSucceeded {
		t.Errorf("状态 = %s, 期望 succeeded", n.Status)
	}
	wantAmt := money.FromCents(990)
	if !n.Amount.Equal(wantAmt) {
		t.Errorf("金额 = %s, 期望 %s", n.Amount, wantAmt)
	}
	if n.OutTradeNo != "AO20260725-xyz" {
		t.Errorf("订单号 = %q", n.OutTradeNo)
	}
	if n.PaymentRef != "20260725ABC" {
		t.Errorf("渠道单号 = %q", n.PaymentRef)
	}
	if n.EventID != "20260725ABC:TRADE_SUCCESS" {
		t.Errorf("幂等键 = %q", n.EventID)
	}
}

func TestParseNotificationEventIDStable(t *testing.T) {
	p := testProvider(t)
	params := map[string]string{
		"pid": "1001", "trade_no": "T-STABLE", "out_trade_no": "O1",
		"money": "1.00", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")

	var first string
	for i := 0; i < 5; i++ {
		n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = n.EventID
		} else if n.EventID != first {
			t.Fatalf("第 %d 次解析的幂等键不同: %q vs %q", i+1, n.EventID, first)
		}
	}
}

func TestParseNotificationRejectsBadSignature(t *testing.T) {
	p := testProvider(t)

	params := map[string]string{
		"pid": "1001", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
		"sign": "00000000000000000000000000000000", "sign_type": "MD5",
	}

	n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
	if err != nil {
		t.Fatalf("解析不该报错，应通过 SignatureVerified 表达: %v", err)
	}
	if n.SignatureVerified {
		t.Error("错误签名被判定为已验证")
	}
}

func TestParseNotificationRejectsForeignMerchant(t *testing.T) {
	p := testProvider(t)

	params := map[string]string{
		"pid": "9999", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")

	n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
	if err != nil {
		t.Fatal(err)
	}
	if n.SignatureVerified {
		t.Error("其他商户号的通知被接受了")
	}
}

func TestParseNotificationRejectsMalformedAmount(t *testing.T) {
	p := testProvider(t)
	params := map[string]string{
		"pid": "1001", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.999", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")

	if _, err := p.ParseNotification(context.Background(), notifyInboundGET(params)); err == nil {
		t.Error("超精度金额应当报错而不是静默截断")
	}
}

func TestParseNotificationAmountExact(t *testing.T) {
	p := testProvider(t)
	base := map[string]string{
		"pid": "1001", "trade_no": "T1", "out_trade_no": "O1",
		"trade_status": "TRADE_SUCCESS",
	}

	ok := map[string]string{}
	for k, v := range base {
		ok[k] = v
	}
	ok["money"] = "9.90"
	ok["sign"] = Sign(ok, "TESTKEY")
	n, err := p.ParseNotification(context.Background(), notifyInboundGET(ok))
	if err != nil {
		t.Fatalf("9.90: %v", err)
	}
	if !n.Amount.Equal(money.FromCents(990)) {
		t.Errorf("9.90 解析为 %s", n.Amount)
	}

	for _, badMoney := range []string{"9.901", "-1", "1e2", ""} {
		bad := map[string]string{}
		for k, v := range base {
			bad[k] = v
		}
		bad["money"] = badMoney
		bad["sign"] = Sign(bad, "TESTKEY")
		if _, err := p.ParseNotification(context.Background(), notifyInboundGET(bad)); err == nil {
			t.Errorf("money=%q 应当报错", badMoney)
		}
	}
}

func TestParseNotificationTamperedAmount(t *testing.T) {
	p := testProvider(t)
	params := map[string]string{
		"pid": "1001", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")
	params["money"] = "9.91"

	n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
	if err != nil {
		t.Fatal(err)
	}
	if n.SignatureVerified {
		t.Error("篡改金额后 SignatureVerified 应为 false")
	}
}

func TestParseNotificationRawDropsSign(t *testing.T) {
	p := testProvider(t)
	params := map[string]string{
		"pid": "1001", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")

	n, err := p.ParseNotification(context.Background(), notifyInboundGET(params))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := n.Raw["sign"]; ok {
		t.Error("Raw 中不应包含 sign 键")
	}
}

func TestCreatePayment(t *testing.T) {
	p := testProvider(t)

	resp, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "AO20260725-t1",
		Amount:     money.FromCents(990),
		Currency:   money.CNY,
		Subject:    "标准套餐 月付",
		Method:     "alipay",
		NotifyURL:  "https://a.example.com/v1/webhooks/payments/epay",
		ReturnURL:  "https://a.example.com/orders",
	})
	if err != nil {
		t.Fatalf("创建支付失败: %v", err)
	}

	u, err := url.Parse(resp.RedirectURL)
	if err != nil {
		t.Fatalf("跳转地址非法: %v", err)
	}
	q := u.Query()

	if q.Get("money") != "9.90" {
		t.Errorf("提交金额 = %q, 期望 9.90", q.Get("money"))
	}
	if q.Get("pid") != "1001" {
		t.Errorf("商户号 = %q", q.Get("pid"))
	}
	if q.Get("sign_type") != "MD5" {
		t.Errorf("sign_type = %q", q.Get("sign_type"))
	}

	got := map[string]string{}
	for k := range q {
		got[k] = q.Get(k)
	}
	if !VerifySign(got, "TESTKEY") {
		t.Error("跳转地址中的签名与参数不匹配")
	}
}

func TestCreatePaymentFormats990(t *testing.T) {
	p := testProvider(t)
	resp, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "X",
		Amount:     money.FromCents(990),
		Currency:   money.CNY,
		NotifyURL:  "https://a.example.com/n",
		ReturnURL:  "https://a.example.com/r",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FormFields["money"] != "9.90" {
		t.Errorf("FormFields[money] = %q", resp.FormFields["money"])
	}
}

func TestCreatePaymentRejectsWrongCurrency(t *testing.T) {
	p := testProvider(t)
	_, err := p.CreatePayment(context.Background(), provider.CreateRequest{
		OutTradeNo: "X",
		Amount:     money.FromCents(100),
		Currency:   money.USD,
	})
	if err == nil {
		t.Error("非人民币应当被拒绝")
	}
}

func TestCreatePaymentRejectsNonPositiveAmount(t *testing.T) {
	p := testProvider(t)
	for _, amt := range []decimal.Decimal{decimal.Zero, decimal.RequireFromString("-0.01")} {
		_, err := p.CreatePayment(context.Background(), provider.CreateRequest{
			OutTradeNo: "X", Amount: amt, Currency: money.CNY,
		})
		if err == nil {
			t.Errorf("金额 %s 应当被拒绝", amt)
		}
	}
}

func TestNotificationAck(t *testing.T) {
	p := testProvider(t)

	okResp := p.NotificationAck(provider.AckInput{SignatureValid: true})
	if string(okResp.Body) != "success" {
		t.Errorf("成功回执 = %q, 易支付要求纯文本 success", string(okResp.Body))
	}
	if !strings.HasPrefix(okResp.ContentType, "text/plain") {
		t.Errorf("回执 Content-Type = %q, 期望 text/plain", okResp.ContentType)
	}
	if okResp.HTTPStatus != http.StatusOK {
		t.Errorf("成功回执状态码 = %d, 期望 200", okResp.HTTPStatus)
	}

	badSig := p.NotificationAck(provider.AckInput{SignatureValid: false})
	if string(badSig.Body) == "success" {
		t.Error("验签失败时不应回 success")
	}
	if badSig.HTTPStatus != http.StatusOK {
		t.Errorf("验签失败仍须回 200，实际 %d", badSig.HTTPStatus)
	}

	herr := p.NotificationAck(provider.AckInput{
		SignatureValid: true,
		HandlerError:   errors.New("订单金额不符"),
	})
	if string(herr.Body) != "fail" {
		t.Errorf("处理失败回执 = %q, 期望 fail", string(herr.Body))
	}
	if herr.HTTPStatus != http.StatusOK {
		t.Errorf("处理失败仍须回 200，实际 %d", herr.HTTPStatus)
	}
}

func TestAckInputOK(t *testing.T) {
	cases := []struct {
		name string
		in   provider.AckInput
		want bool
	}{
		{"验签通过且处理成功", provider.AckInput{SignatureValid: true}, true},
		{"验签失败", provider.AckInput{SignatureValid: false}, false},
		{"处理出错", provider.AckInput{SignatureValid: true, HandlerError: errors.New("x")}, false},
		{"两者都失败", provider.AckInput{HandlerError: errors.New("x")}, false},
	}
	for _, c := range cases {
		if got := c.in.OK(); got != c.want {
			t.Errorf("%s: OK() = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestNewRejectsInsecureConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"缺商户号", Config{BaseURL: "https://p.example.com", Key: "K"}},
		{"缺密钥", Config{BaseURL: "https://p.example.com", MerchantID: "1"}},
		{"非 https", Config{BaseURL: "http://p.example.com", MerchantID: "1", Key: "K"}},
		{"URL 非法", Config{BaseURL: "://bad", MerchantID: "1", Key: "K"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := New(c.cfg); err == nil {
				t.Errorf("配置 %q 应当被拒绝", c.name)
			}
		})
	}
}

func TestSanitizeSubject(t *testing.T) {
	cases := map[string]string{
		"标准套餐":      "标准套餐",
		"A&B":       "A＆B",
		"x=y":       "x＝y",
		"":          "订阅服务",
		"  ":        "订阅服务",
		"line1\nl2": "line1 l2",
	}
	for in, want := range cases {
		if got := sanitizeSubject(in); got != want {
			t.Errorf("sanitizeSubject(%q) = %q, 期望 %q", in, got, want)
		}
	}

	long := strings.Repeat("套", 100)
	if got := []rune(sanitizeSubject(long)); len(got) != 64 {
		t.Errorf("超长商品名未截断到 64 字符，实际 %d", len(got))
	}
}

func TestQueryPaymentParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "order" {
			t.Errorf("act = %q", r.URL.Query().Get("act"))
		}
		fmt.Fprint(w, `{"code":1,"msg":"success","trade_no":"T-999",`+
			`"out_trade_no":"AO-1","type":"alipay","money":"9.90","status":1}`)
	}))
	defer srv.Close()

	p := testProvider(t)
	p.cfg.BaseURL = srv.URL
	p.cfg.APIPath = "/api.php"

	res, err := p.QueryPayment(context.Background(), "AO-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !res.Found {
		t.Fatal("应当找到订单")
	}
	if res.Status != provider.StatusSucceeded {
		t.Errorf("状态 = %s", res.Status)
	}
	if !res.Amount.Equal(money.FromCents(990)) {
		t.Errorf("金额 = %s, 期望 9.90", res.Amount)
	}
}

func TestQueryPaymentHandlesNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"msg":"订单不存在"}`)
	}))
	defer srv.Close()

	p := testProvider(t)
	p.cfg.BaseURL = srv.URL

	res, err := p.QueryPayment(context.Background(), "NOPE")
	if err != nil {
		t.Fatalf("订单不存在不应报错: %v", err)
	}
	if res.Found {
		t.Error("不存在的订单被判定为找到")
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("dial tcp: connection refused")
}

func TestQueryPaymentTransportErrorOmitsKey(t *testing.T) {
	p := testProvider(t)
	p.client = &http.Client{Transport: failingTransport{}}
	_, err := p.QueryPayment(context.Background(), "ORDER1")
	if err == nil {
		t.Fatal("want a transport error")
	}
	if strings.Contains(err.Error(), "TESTKEY") || !strings.Contains(err.Error(), "pay.example.com") {
		t.Fatalf("error = %q", err)
	}
}

func TestCheckDialAddr(t *testing.T) {
	blocked := []string{
		"127.0.0.1:443", "10.1.2.3:443", "172.16.0.1:443", "192.168.1.1:443",
		"169.254.169.254:80", "100.100.100.200:80", "100.64.0.1:443", "0.0.0.0:443",
		"0.1.2.3:443", "224.0.0.1:443", "255.255.255.255:443", "198.18.0.1:443",
		"[::1]:443", "[fc00::1]:443", "[fd00:ec2::254]:80", "[fe80::1]:443", "[::]:443",
		"[::ffff:127.0.0.1]:443", "[64:ff9b::a00:1]:443", "[ff02::1]:443",
	}
	for _, a := range blocked {
		if err := checkDialAddr(a); err == nil {
			t.Errorf("%s 应当被拒绝", a)
		}
	}
	for _, a := range []string{"203.0.113.10:443", "[2001:db8::1]:443", "8.8.8.8:443"} {
		if err := checkDialAddr(a); err != nil {
			t.Errorf("%s 不应被拒绝: %v", a, err)
		}
	}
}

func TestQueryPaymentRefusesPrivateHostAtDial(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		fmt.Fprint(w, `{"code":1}`)
	}))
	defer srv.Close()

	// 不允许内网：New 不再解析域名，拦截发生在拨号时
	p, err := New(Config{BaseURL: "https://pay.example.test", MerchantID: "1001", Key: "TESTKEY"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.cfg.BaseURL = srv.URL
	if _, err := p.QueryPayment(context.Background(), "AO-1"); err == nil {
		t.Fatal("连 127.0.0.1 应当失败")
	} else if strings.Contains(err.Error(), "TESTKEY") {
		t.Fatalf("错误里不能带密钥: %v", err)
	}
	if hit {
		t.Fatal("请求不应到达内网服务")
	}
}

func TestQueryPaymentRejectsForeignOutTradeNo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":1,"trade_no":"T-1","out_trade_no":"OTHER","type":"alipay","money":"9.90","status":1}`)
	}))
	defer srv.Close()

	p := testProvider(t)
	p.cfg.BaseURL = srv.URL
	res, err := p.QueryPayment(context.Background(), "AO-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if res.Found {
		t.Fatal("对端回了别的单号，应按没找到处理")
	}
}
