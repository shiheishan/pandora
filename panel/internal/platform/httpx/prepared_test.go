package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type preparedFixture struct {
	OrderID  string `json:"order_id"`
	OrderNo  string `json:"order_no"`
	Amount   int64  `json:"amount"`
	Currency string `json:"currency"`
}

func TestPrepareJSONAndWritePreparedUseTheSameExactBytes(t *testing.T) {
	value := preparedFixture{
		OrderID:  "91000000-0000-7000-8000-000000000201",
		OrderNo:  "TOPUP-1",
		Amount:   100,
		Currency: "CNY",
	}
	prepared, err := PrepareJSON(http.StatusOK, value)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("{\"order_id\":\"91000000-0000-7000-8000-000000000201\",\"order_no\":\"TOPUP-1\",\"amount\":100,\"currency\":\"CNY\"}\n")
	if got := prepared.BodyBytes(); string(got) != string(want) {
		t.Fatalf("prepared body = %q, want %q", got, want)
	}
	mutated := prepared.BodyBytes()
	mutated[0] = 'X'
	if got := prepared.BodyBytes(); string(got) != string(want) {
		t.Fatal("BodyBytes exposed mutable prepared response storage")
	}

	recorder := httptest.NewRecorder()
	WritePrepared(recorder, prepared)
	if recorder.Code != http.StatusOK || recorder.Body.String() != string(want) {
		t.Fatalf("written response = %d/%q", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/json; charset=utf-8" {
		t.Fatalf("Content-Type = %q", got)
	}
	if got := recorder.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestPrepareJSONRejectsInvalidStatusAndEncodingFailure(t *testing.T) {
	if _, err := PrepareJSON(99, struct{}{}); err == nil {
		t.Fatal("invalid status was accepted")
	}
	if _, err := PrepareJSON(http.StatusOK, make(chan int)); err == nil {
		t.Fatal("unencodable JSON was accepted")
	}
}
