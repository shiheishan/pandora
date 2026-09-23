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

	"github.com/aegispanel/aegis/internal/domain/payment"
)

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

//------------------------------------------------------------------------------
// 签名串构造 —— 签名对不上时问题几乎总在这里
//------------------------------------------------------------------------------

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

	// ASCII 升序：money < name < notify_url < out_trade_no < pid < return_url < type
	want := "money=9.90&name=标准套餐&notify_url=https://a.example.com/notify" +
		"&out_trade_no=AO20260725-abc&pid=1001&return_url=https://a.example.com/return" +
		"&type=alipay" + "SECRETKEY"

	if got := signPayload(params, "SECRETKEY"); got != want {
		t.Errorf("签名串不符\n got: %s\nwant: %s", got, want)
	}
}

// 中文与 URL 必须保持原样。用 url.Values.Encode() 是最常见的错误实现。
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

// 空值参数必须剔除，否则会多出 foo= 段导致签名不符。
func TestSignPayloadDropsEmptyAndSignFields(t *testing.T) {
	params := map[string]string{
		"pid":        "1001",
		"money":      "1.00",
		"return_url": "", // 空值
		"sign":       "deadbeef",
		"sign_type":  "MD5",
	}
	want := "money=1.00&pid=1001" + "K"
	if got := signPayload(params, "K"); got != want {
		t.Errorf("空值或 sign 字段未被剔除\n got: %s\nwant: %s", got, want)
	}
}

// 密钥是直接追加，不是 &key=xxx —— 这是易支付与部分其他协议的关键区别。
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

	// 大写十六进制也应接受：部分易支付站点返回大写
	upper := map[string]string{}
	for k, v := range params {
		upper[k] = v
	}
	upper["sign"] = strings.ToUpper(params["sign"])
	if !VerifySign(upper, "K") {
		t.Error("大写签名未通过校验")
	}

	// 篡改金额必须失败 —— 这是防篡改的核心
	tampered := map[string]string{}
	for k, v := range params {
		tampered[k] = v
	}
	tampered["money"] = "0.01"
	if VerifySign(tampered, "K") {
		t.Error("金额被篡改后签名仍通过，防篡改失效")
	}

	// 错误密钥必须失败
	if VerifySign(params, "WRONG") {
		t.Error("错误密钥下签名仍通过")
	}

	// 缺签名必须失败
	noSign := map[string]string{"pid": "1001"}
	if VerifySign(noSign, "K") {
		t.Error("无签名参数通过了校验")
	}
}

//------------------------------------------------------------------------------
// 通知解析
//------------------------------------------------------------------------------

func testProvider(t *testing.T) *Provider {
	t.Helper()
	p, err := New(Config{
		Code:             "epay",
		BaseURL:          "https://pay.example.com",
		MerchantID:       "1001",
		Key:              "TESTKEY",
		AllowPrivateHost: true, // 测试不做 DNS 解析校验
	})
	if err != nil {
		t.Fatalf("构造 Provider 失败: %v", err)
	}
	return p
}

func notifyRequest(params map[string]string) *http.Request {
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return httptest.NewRequest(http.MethodGet, "/notify?"+q.Encode(), nil)
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

	n, err := p.ParseNotification(context.Background(), notifyRequest(params))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}

	if !n.SignatureVerified {
		t.Error("正确签名的通知未通过验签")
	}
	if n.Status != payment.StatusSucceeded {
		t.Errorf("状态 = %s, 期望 succeeded", n.Status)
	}
	// 9.90 元必须精确等于 990 分
	if n.Amount != 990 {
		t.Errorf("金额 = %d, 期望 990（9.90 元）", n.Amount)
	}
	if n.OutTradeNo != "AO20260725-xyz" {
		t.Errorf("订单号 = %q", n.OutTradeNo)
	}
	if n.PaymentRef != "20260725ABC" {
		t.Errorf("渠道单号 = %q", n.PaymentRef)
	}
	// 幂等键必须对「同一事实」稳定
	if n.EventID != "20260725ABC:TRADE_SUCCESS" {
		t.Errorf("幂等键 = %q", n.EventID)
	}
}

// 同一通知重复投递必须得到相同的幂等键，这是 PAY-003 在渠道层的前提。
func TestParseNotificationEventIDStable(t *testing.T) {
	p := testProvider(t)
	params := map[string]string{
		"pid": "1001", "trade_no": "T-STABLE", "out_trade_no": "O1",
		"money": "1.00", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY")

	var first string
	for i := 0; i < 5; i++ {
		n, err := p.ParseNotification(context.Background(), notifyRequest(params))
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

	n, err := p.ParseNotification(context.Background(), notifyRequest(params))
	if err != nil {
		t.Fatalf("解析不该报错，应通过 SignatureVerified 表达: %v", err)
	}
	if n.SignatureVerified {
		t.Error("错误签名被判定为已验证")
	}
}

// 别人拿自己的易支付账号往我们的回调地址推通知：pid 不匹配必须拒绝。
func TestParseNotificationRejectsForeignMerchant(t *testing.T) {
	p := testProvider(t)

	params := map[string]string{
		"pid": "9999", "trade_no": "T1", "out_trade_no": "O1",
		"money": "9.90", "trade_status": "TRADE_SUCCESS",
	}
	params["sign"] = Sign(params, "TESTKEY") // 签名本身有效

	n, err := p.ParseNotification(context.Background(), notifyRequest(params))
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
		"money": "9.999", "trade_status": "TRADE_SUCCESS", // 超精度
	}
	params["sign"] = Sign(params, "TESTKEY")

	if _, err := p.ParseNotification(context.Background(), notifyRequest(params)); err == nil {
		t.Error("超精度金额应当报错而不是静默截断")
	}
}

//------------------------------------------------------------------------------
// 发起支付
//------------------------------------------------------------------------------

func TestCreatePayment(t *testing.T) {
	p := testProvider(t)

	resp, err := p.CreatePayment(context.Background(), payment.CreateRequest{
		OutTradeNo: "AO20260725-t1",
		Amount:     990,
		Currency:   "CNY",
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
		t.Errorf("提交金额 = %q, 期望 9.90（990 分）", q.Get("money"))
	}
	if q.Get("pid") != "1001" {
		t.Errorf("商户号 = %q", q.Get("pid"))
	}
	if q.Get("sign_type") != "MD5" {
		t.Errorf("sign_type = %q", q.Get("sign_type"))
	}

	// 用跳转地址里的参数反向验签，确认签名与实际提交的内容一致
	got := map[string]string{}
	for k := range q {
		got[k] = q.Get(k)
	}
	if !VerifySign(got, "TESTKEY") {
		t.Error("跳转地址中的签名与参数不匹配")
	}
}

func TestCreatePaymentRejectsWrongCurrency(t *testing.T) {
	p := testProvider(t)
	_, err := p.CreatePayment(context.Background(), payment.CreateRequest{
		OutTradeNo: "X", Amount: 100, Currency: "USD",
	})
	if err == nil {
		t.Error("非人民币应当被拒绝")
	}
}

func TestCreatePaymentRejectsNonPositiveAmount(t *testing.T) {
	p := testProvider(t)
	for _, amt := range []int64{0, -1} {
		_, err := p.CreatePayment(context.Background(), payment.CreateRequest{
			OutTradeNo: "X", Amount: amt, Currency: "CNY",
		})
		if err == nil {
			t.Errorf("金额 %d 应当被拒绝", amt)
		}
	}
}

//------------------------------------------------------------------------------
// 回执与不支持的操作
//------------------------------------------------------------------------------

func TestNotificationAck(t *testing.T) {
	p := testProvider(t)

	okResp := p.NotificationAck(payment.AckInput{SignatureValid: true})
	if string(okResp.Body) != "success" {
		t.Errorf("成功回执 = %q, 易支付要求纯文本 success", string(okResp.Body))
	}
	if !strings.HasPrefix(okResp.ContentType, "text/plain") {
		t.Errorf("回执 Content-Type = %q, 期望 text/plain", okResp.ContentType)
	}
	if okResp.HTTPStatus != http.StatusOK {
		t.Errorf("成功回执状态码 = %d, 期望 200", okResp.HTTPStatus)
	}

	// 验签失败
	badSig := p.NotificationAck(payment.AckInput{SignatureValid: false})
	if string(badSig.Body) == "success" {
		t.Error("验签失败时不应回 success")
	}
	// 关键：易支付对非 2xx 会无限重推，而签名错误重推一万次也不会变好
	if badSig.HTTPStatus != http.StatusOK {
		t.Errorf("验签失败仍须回 200（否则易支付无限重推），实际 %d", badSig.HTTPStatus)
	}

	// 业务处理失败
	herr := p.NotificationAck(payment.AckInput{
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
		in   payment.AckInput
		want bool
	}{
		{"验签通过且处理成功", payment.AckInput{SignatureValid: true}, true},
		{"验签失败", payment.AckInput{SignatureValid: false}, false},
		{"处理出错", payment.AckInput{SignatureValid: true, HandlerError: errors.New("x")}, false},
		{"两者都失败", payment.AckInput{HandlerError: errors.New("x")}, false},
	}
	for _, c := range cases {
		if got := c.in.OK(); got != c.want {
			t.Errorf("%s: OK() = %v, 期望 %v", c.name, got, c.want)
		}
	}
}

func TestRefundNotSupported(t *testing.T) {
	p := testProvider(t)
	_, err := p.Refund(context.Background(), payment.RefundRequest{
		PaymentRef: "T1", Amount: 990, Currency: "CNY",
	})
	if err == nil {
		t.Fatal("易支付无退款接口，应当明确报错而不是假装成功")
	}
	if !strings.Contains(err.Error(), "不支持") {
		t.Errorf("错误信息应说明不支持: %v", err)
	}
}

//------------------------------------------------------------------------------
// 配置校验
//------------------------------------------------------------------------------

func TestNewRejectsInsecureConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"缺商户号", Config{Code: "e", BaseURL: "https://p.example.com", Key: "K"}},
		{"缺密钥", Config{Code: "e", BaseURL: "https://p.example.com", MerchantID: "1"}},
		{"非 https", Config{Code: "e", BaseURL: "http://p.example.com", MerchantID: "1", Key: "K"}},
		{"URL 非法", Config{Code: "e", BaseURL: "://bad", MerchantID: "1", Key: "K"}},
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
		"A&B":       "A＆B", // & 会破坏签名串结构
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

func TestExponentForCNY(t *testing.T) {
	if e := payment.Exponent("CNY"); e != 2 {
		t.Errorf("人民币最小单位位数 = %d, 期望 2", e)
	}
	if got := payment.FormatMinor(990, 2); got != "9.90" {
		t.Errorf("990 分格式化 = %q, 期望 9.90", got)
	}
}

func TestQueryPaymentParsesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("act") != "order" {
			t.Errorf("act = %q", r.URL.Query().Get("act"))
		}
		fmt.Fprint(w, `{"code":1,"msg":"success","trade_no":"T-999",
			"out_trade_no":"AO-1","type":"alipay","money":"9.90","status":1}`)
	}))
	defer srv.Close()

	p := testProvider(t)
	p.cfg.BaseURL = srv.URL // 覆盖为测试服务器
	p.cfg.APIPath = "/api.php"

	res, err := p.QueryPayment(context.Background(), "AO-1")
	if err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	if !res.Found {
		t.Fatal("应当找到订单")
	}
	if res.Status != payment.StatusSucceeded {
		t.Errorf("状态 = %s", res.Status)
	}
	if res.Amount != 990 {
		t.Errorf("金额 = %d, 期望 990", res.Amount)
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
