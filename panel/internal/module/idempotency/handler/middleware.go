// 幂等中间件在 handler 返回后，根据响应完成或释放认领。
// 若业务在同一数据库事务内已调用 service.Complete* 并成功提交，Claim.Completed() 为真，本中间件不再写库。
// 否则由本中间件按录下的响应：2xx 可重放则 Complete，2xx 不可重放则 CompleteUnreplayable，非 2xx 则 Release。
package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/aegispanel/aegis/internal/module/idempotency/service"
	"github.com/aegispanel/aegis/internal/pkg/authn"
	"github.com/aegispanel/aegis/internal/pkg/errs"
	"github.com/aegispanel/aegis/internal/pkg/httpx"
	plog "github.com/aegispanel/aegis/internal/pkg/log"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Service 是中间件用到的 idempotency 服务（*service.Service 满足）。
type Service interface {
	Claim(ctx context.Context, scope, key string, fp service.Fingerprint) (*service.Claim, *service.Response, error)
	Complete(ctx context.Context, c *service.Claim, r service.Response) error
	CompleteUnreplayable(ctx context.Context, c *service.Claim, status int) error
	Release(ctx context.Context, c *service.Claim, status int) error
}

// Handler 挂载按 scope 区分的幂等中间件。
type Handler struct {
	svc   Service
	fpKey []byte
}

// New 构造 Handler；fpKey 是请求指纹的 HMAC 密钥（主密钥按 crypto.InfoHMACIdempotency 派生），不能为空。
func New(svc Service, fpKey []byte) *Handler {
	if len(fpKey) == 0 {
		panic("idempotency: 指纹密钥为空")
	}
	return &Handler{svc: svc, fpKey: fpKey}
}

// Middleware 返回认领、重放与事后完成/释放的 gin 中间件。
func (h *Handler) Middleware(scope string) gin.HandlerFunc {
	if !service.ValidScope(scope) {
		panic(fmt.Sprintf("idempotency: invalid scope %q", scope))
	}
	return func(c *gin.Context) {
		key := c.GetHeader(service.HeaderKey)
		if !service.ValidKey(key) {
			httpx.Fail(c, errs.Newf(errs.BadRequest, "此接口要求有效的 Idempotency-Key 请求头"))
			return
		}

		p, ok := authn.FromGin(c)
		if !ok || p.UserID == uuid.Nil {
			httpx.Fail(c, errs.Newf(errs.Unauthorized, "该接口要求有效的已认证账号"))
			return
		}

		bodyReader := c.Request.Body
		if bodyReader == nil {
			bodyReader = http.NoBody
		}
		body, err := io.ReadAll(http.MaxBytesReader(c.Writer, bodyReader, service.MaxBodyBytes))
		if err != nil {
			httpx.Fail(c, errs.Newf(errs.BadRequest, "读取请求体失败"))
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(body))

		ctx := c.Request.Context()
		fp := service.NewFingerprint(h.fpKey, c.Request.Method, service.Target(c.Request.URL), body)
		claim, replay, err := h.svc.Claim(ctx, scope, key, fp)
		if err != nil {
			httpx.Fail(c, err)
			return
		}
		if replay != nil {
			c.Header(service.HeaderReplayed, "true")
			replay.Serve(c.Writer)
			c.Abort()
			return
		}

		c.Request = c.Request.WithContext(service.WithClaim(ctx, claim))
		orig := c.Writer
		rec := &recorder{ResponseWriter: orig}
		c.Writer = rec
		defer func() {
			// 业务 panic 时 finish 不会执行，先释放认领再交给外层 Recovery，否则这个键要等 30 天保留期才能再用
			if r := recover(); r != nil {
				c.Writer = orig
				h.release(c, scope, claim, http.StatusInternalServerError)
				panic(r)
			}
		}()
		c.Next()
		c.Writer = orig
		h.finish(c, scope, claim, rec, orig)
	}
}

func (h *Handler) release(c *gin.Context, scope string, claim *service.Claim, status int) {
	if claim.Completed() {
		return
	}
	wctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()
	if err := h.svc.Release(wctx, claim, status); err != nil {
		plog.From(c.Request.Context()).Error("idempotency release failed",
			zap.String("scope", scope),
			zap.String("claim_id", claim.ID.String()),
			zap.Int("status", status),
			zap.Error(err),
		)
	}
}

func (h *Handler) finish(c *gin.Context, scope string, claim *service.Claim, rec *recorder, orig gin.ResponseWriter) {
	if claim.Completed() {
		return
	}

	wctx, cancel := context.WithTimeout(context.WithoutCancel(c.Request.Context()), 5*time.Second)
	defer cancel()

	ctx := c.Request.Context()
	logStatus := 0
	var finishErr error

	if len(c.Errors) > 0 && !orig.Written() {
		e := errs.As(c.Errors.Last().Err)
		logStatus = e.Status()
		finishErr = h.svc.Release(wctx, claim, logStatus)
	} else {
		status, resp, ok := rec.result()
		logStatus = status
		if status >= 200 && status < 300 {
			if ok {
				resp.Status = status
				finishErr = h.svc.Complete(wctx, claim, resp)
			} else {
				finishErr = h.svc.CompleteUnreplayable(wctx, claim, status)
			}
		} else {
			finishErr = h.svc.Release(wctx, claim, status)
		}
	}

	if finishErr != nil {
		plog.From(ctx).Error("idempotency completion failed",
			zap.String("scope", scope),
			zap.String("claim_id", claim.ID.String()),
			zap.Int("status", logStatus),
			zap.Error(finishErr),
		)
	}
}
