package billing

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/aegispanel/aegis/internal/domain/payment"
)

// queryStubProvider 按 out_trade_no 返回预先编排的查单结果，未编排的单回「未找到」。
type queryStubProvider struct {
	code  string
	delay time.Duration

	mu      sync.Mutex
	results map[string]*payment.QueryResult
	errs    map[string]error
	calls   map[string]int
}

func newQueryStubProvider(code string) *queryStubProvider {
	return &queryStubProvider{code: code, results: map[string]*payment.QueryResult{},
		errs: map[string]error{}, calls: map[string]int{}}
}

func (p *queryStubProvider) answer(orderNo string, res *payment.QueryResult, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.results[orderNo], p.errs[orderNo] = res, err
}

func (p *queryStubProvider) callCount(orderNo string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[orderNo]
}

func (p *queryStubProvider) Code() string { return p.code }

func (p *queryStubProvider) CreatePayment(context.Context, payment.CreateRequest) (*payment.CreateResponse, error) {
	return &payment.CreateResponse{HTTPMethod: http.MethodGet, RedirectURL: "https://pay.example.test/cashier"}, nil
}

func (p *queryStubProvider) ParseNotification(context.Context, *http.Request) (*payment.Notification, error) {
	return nil, payment.ErrNotSupported
}

func (p *queryStubProvider) NotificationAck(payment.AckInput) payment.AckOutput {
	return payment.AckOutput{HTTPStatus: http.StatusOK}
}

func (p *queryStubProvider) QueryPayment(ctx context.Context, orderNo string) (*payment.QueryResult, error) {
	p.mu.Lock()
	p.calls[orderNo]++
	res, err := p.results[orderNo], p.errs[orderNo]
	p.mu.Unlock()
	if p.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(p.delay):
		}
	}
	if err != nil {
		return nil, err
	}
	if res == nil {
		return &payment.QueryResult{Found: false}, nil
	}
	copied := *res
	return &copied, nil
}

func (p *queryStubProvider) Refund(context.Context, payment.RefundRequest) (*payment.RefundResult, error) {
	return nil, payment.ErrNotSupported
}
