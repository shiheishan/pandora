// [INPUT]: 依赖 client.go 的 login / do，依赖 traffic.go 的 actor、adminActor、firstSubscriptionID、prefixFromLinks，依赖 ltkit.Recorder
// [OUTPUT]: 对外提供 包内的 warmup：登录后台操作员与门户活跃池、取每人的订阅 id、取租户订阅前缀
// [POS]: tools/loadtest/userload 的预热段：在计时开始前把「登录一次」的贵活（Argon2）做完，正式运行只复用令牌；预热自己的计量写 users-warmup.json，登录哈希的耗时看这里
// [PROTOCOL]: 变更时更新此头部，然后检查 CLAUDE.md

package userload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/aegis/tools/loadtest/ltkit"
)

func warmup(ctx context.Context, cfg usersConfig, t *traffic, rec *ltkit.Recorder, stdout io.Writer) error {
	t.rec = rec
	start := time.Now()
	if cfg.adminRate > 0 {
		for _, ip := range cfg.adminIPs {
			tok, resp := t.c.login(ctx, rec, t.adm, cfg.admin, ip)
			if tok == "" {
				return fmt.Errorf("users: admin login from %s failed: %s", ip, describe(resp))
			}
			t.admins = append(t.admins, &adminActor{ip: ip, token: tok})
		}
	}

	if len(t.pool) > 0 {
		ok, failed := loginPool(ctx, cfg, t, rec)
		fmt.Fprintf(stdout, "[users] warm-up: %d/%d pool users logged in (%d failed) in %s\n",
			ok, len(t.pool), failed, time.Since(start).Round(time.Millisecond))
		if ok == 0 {
			return errors.New("users: no pool user could log in; see users-warmup.txt for the status codes")
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}

	if cfg.subRate > 0 && t.prefix == "" {
		prefix, err := discoverPrefix(ctx, t, rec)
		if err != nil {
			return err
		}
		t.prefix = prefix
	}
	return nil
}

// loginPool 按 -warmup-login-rate 定速登录活跃池：登录是 Argon2，一拥而上只会把预热变成
// 一次登录洪峰，还会撞上 auth_net 的 /24 限流。每人登录后顺手取一次自己的订阅 id。
func loginPool(ctx context.Context, cfg usersConfig, t *traffic, rec *ltkit.Recorder) (ok, failed int) {
	var nOK, nFail atomic.Int64
	sem := make(chan struct{}, cfg.maxInflight)
	var wg sync.WaitGroup
	interval := time.Duration(float64(time.Second) / cfg.warmupLoginRate)
	begin := time.Now()
	for i, a := range t.pool {
		if wait := time.Until(begin.Add(time.Duration(i) * interval)); wait > 0 {
			select {
			case <-ctx.Done():
			case <-time.After(wait):
			}
		}
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			tok, _ := t.c.login(ctx, rec, t.pub, credentials{a.u.Email, t.pass}, a.u.RealIP)
			if tok == "" {
				nFail.Add(1)
				return
			}
			a.setToken(tok)
			nOK.Add(1)
			resp := t.c.do(ctx, rec, request{
				gw: t.pub, method: http.MethodGet, path: "/v1/me/subscriptions", tmpl: "/v1/me/subscriptions",
				ip: a.u.RealIP, ua: browserUA, token: tok,
			})
			if id := firstSubscriptionID(resp.body); resp.status == http.StatusOK && id != "" {
				a.subID.Store(&id)
			}
		}()
	}
	wg.Wait()
	return int(nOK.Load()), int(nFail.Load())
}

// discoverPrefix 从一个已登录用户的订阅链接里读租户前缀（-sub-prefix 没给时）。
// manifest 只有令牌没有前缀：前缀是租户级的部署值，不进 manifest 也不进报告。
func discoverPrefix(ctx context.Context, t *traffic, rec *ltkit.Recorder) (string, error) {
	var a *actor
	for _, p := range t.pool {
		if p.currentToken() != "" {
			a = p
			break
		}
	}
	if a == nil {
		a = t.users[0]
		tok, resp := t.c.login(ctx, rec, t.pub, credentials{a.u.Email, t.pass}, a.u.RealIP)
		if tok == "" {
			return "", fmt.Errorf("users: login to read the subscription prefix failed: %s (or pass -sub-prefix)", describe(resp))
		}
		a.setToken(tok)
	}
	resp := t.c.do(ctx, rec, request{
		gw: t.pub, method: http.MethodGet, path: "/v1/me/subscription-links", tmpl: "/v1/me/subscription-links",
		ip: a.u.RealIP, ua: browserUA, token: a.currentToken(),
	})
	if resp.status != http.StatusOK {
		return "", fmt.Errorf("users: reading subscription links failed: %s (or pass -sub-prefix)", describe(resp))
	}
	prefix := prefixFromLinks(resp.body)
	if prefix == "" {
		return "", errors.New("users: the first user has no subscription link to read the prefix from; pass -sub-prefix")
	}
	return prefix, nil
}
