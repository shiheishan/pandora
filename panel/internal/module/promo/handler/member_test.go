package handler

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/aegispanel/aegis/internal/module/promo/service"
	"github.com/aegispanel/aegis/internal/pkg/errs"
)

type memberFake struct {
	Service
	ofFn    func(ctx context.Context, userID uuid.UUID) (service.MemberDiscountView, error)
	setFn   func(ctx context.Context, userID uuid.UUID, percent int) (service.MemberDiscountView, error)
	clearFn func(ctx context.Context, userID uuid.UUID) error
}

func (f *memberFake) MemberDiscountOf(ctx context.Context, userID uuid.UUID) (service.MemberDiscountView, error) {
	if f.ofFn != nil {
		return f.ofFn(ctx, userID)
	}
	return service.MemberDiscountView{UserID: userID}, nil
}

func (f *memberFake) SetMemberDiscount(ctx context.Context, userID uuid.UUID, percent int) (service.MemberDiscountView, error) {
	if f.setFn != nil {
		return f.setFn(ctx, userID, percent)
	}
	now := time.Now().UTC()
	return service.MemberDiscountView{
		UserID:    userID,
		Percent:   &percent,
		UpdatedAt: &now,
	}, nil
}

func (f *memberFake) ClearMemberDiscount(ctx context.Context, userID uuid.UUID) error {
	if f.clearFn != nil {
		return f.clearFn(ctx, userID)
	}
	return nil
}

func TestMemberDiscountHandler(t *testing.T) {
	t.Run("GET 没设置时 percent 为 null", func(t *testing.T) {
		u := uuid.New()
		f := &memberFake{
			ofFn: func(_ context.Context, uid uuid.UUID) (service.MemberDiscountView, error) {
				return service.MemberDiscountView{UserID: uid, Percent: nil}, nil
			},
		}
		e := newEngine(f, true, nil)
		w := do(e, http.MethodGet, "/v1/member-discounts/"+u.String(), "")
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		got := decMap(t, w)
		if got["user_id"] != u.String() {
			t.Fatalf("user_id=%v 期望 %s", got["user_id"], u.String())
		}
		if got["percent"] != nil {
			t.Fatalf("percent 期望 null，实际 %v", got["percent"])
		}
	})

	t.Run("PUT 正常传参并返回设置后的折扣", func(t *testing.T) {
		u := uuid.New()
		var passedUser uuid.UUID
		var passedPercent int
		now := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
		f := &memberFake{
			setFn: func(_ context.Context, uid uuid.UUID, p int) (service.MemberDiscountView, error) {
				passedUser = uid
				passedPercent = p
				return service.MemberDiscountView{
					UserID:    uid,
					Percent:   &p,
					UpdatedAt: &now,
				}, nil
			},
		}
		e := newEngine(f, true, nil)
		w := do(e, http.MethodPut, "/v1/member-discounts/"+u.String(), `{"percent":10}`)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		if passedUser != u || passedPercent != 10 {
			t.Fatalf("传递给 service 的参数 user=%v percent=%d", passedUser, passedPercent)
		}
		got := decMap(t, w)
		if got["user_id"] != u.String() {
			t.Fatalf("user_id=%v 期望 %s", got["user_id"], u.String())
		}
		p, ok := got["percent"].(float64)
		if !ok || int(p) != 10 {
			t.Fatalf("percent=%v 期望 10", got["percent"])
		}
	})

	t.Run("PUT service 返回 errs.Invalid 得 422", func(t *testing.T) {
		u := uuid.New()
		f := &memberFake{
			setFn: func(_ context.Context, _ uuid.UUID, _ int) (service.MemberDiscountView, error) {
				return service.MemberDiscountView{}, errs.Invalid(map[string]string{"percent": "请填 1 到 100 的整数（10 表示打九折）"})
			},
		}
		e := newEngine(f, true, nil)
		w := do(e, http.MethodPut, "/v1/member-discounts/"+u.String(), `{"percent":101}`)
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status=%d 期望 422, body=%s", w.Code, w.Body.String())
		}
		code := errCode(t, w)
		if code != string(errs.ValidationFailed) {
			t.Fatalf("code=%s 期望 %s", code, errs.ValidationFailed)
		}
	})

	t.Run("DELETE 回 ok:true", func(t *testing.T) {
		u := uuid.New()
		f := &memberFake{}
		e := newEngine(f, true, nil)
		w := do(e, http.MethodDelete, "/v1/member-discounts/"+u.String(), "")
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		got := decMap(t, w)
		if got["ok"] != true {
			t.Fatalf("ok=%v 期望 true", got["ok"])
		}
	})

	t.Run("user_id 不是 uuid 回 404", func(t *testing.T) {
		f := &memberFake{}
		e := newEngine(f, true, nil)

		for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
			var body string
			if m == http.MethodPut {
				body = `{"percent":10}`
			}
			w := do(e, m, "/v1/member-discounts/not-a-uuid", body)
			if w.Code != http.StatusNotFound {
				t.Fatalf("方法 %s status=%d 期望 404", m, w.Code)
			}
		}
	})
}
