package certs

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/aegispanel/nodeagent/core"
	"github.com/go-acme/lego/v5/acme"
	legolog "github.com/go-acme/lego/v5/log"
)

// Options 是 Manager 的配置。只有 Dir 必填。
type Options struct {
	Dir          string           // <cache_dir>/acme，生产 /var/lib/pandora-native/acme
	DirectoryURL string           // 空 = lego.DirectoryURLLetsEncrypt（LE 生产）
	HTTPPort     int              // 0 = 80；只给验收与测试改
	RootCAs      *x509.CertPool   // nil = 系统根；只给测试（pebble 的根）与验收（ca_file）
	Logger       *slog.Logger     // nil = slog.Default()
	Now          func() time.Time // nil = time.Now；测试注入
}

// Manager 按标识符自动申请与续期入站证书。
type Manager struct {
	opts          Options
	log           *slog.Logger
	now           func() time.Time
	store         *store
	mu            sync.Mutex
	entries       map[string]*entry // 键：规整后的标识符
	wanted        map[string]bool
	certs         atomic.Pointer[map[string]*tls.Certificate] // 写时复制，Certificate 只做原子读
	issuer        *issuer                                     // 懒建：第一次要下单时才建（注册账户要联网）
	forceRegister bool                                        // 账户在 CA 那边不存在时置真，下次 newIssuer 强制重新注册
	gen           uint64                                      // Purge 加一；下单回来时 gen 变了就丢弃结果
	wake          chan struct{}                               // 容量 1
	updates       chan string                                 // 容量 16
}

type entry struct {
	id          string
	cert        *storedCert // nil = 还没有证书
	renewAt     time.Time   // 有证书时：何时续期（先用 fallbackRenewAt，ARI 查到后改成 ARI 的）
	ariNext     time.Time   // 有证书时：下次查 ARI 的时刻
	nextAttempt time.Time   // 申请或续期失败后的下次重试时刻；零值 = 不受退避限制
	failures    int         // 连续失败次数，成功清零
	errCode     string
	errDetail   string
	unusedSince time.Time // 不在 Want 集合里的起始时刻；在集合里时为零值
}

const (
	stepIssue = 1
	stepPurge = 2
	stepARI   = 3
)

var _ core.CertSource = (*Manager)(nil)

func New(opts Options) (*Manager, error) {
	if strings.TrimSpace(opts.Dir) == "" {
		return nil, errors.New("证书落盘目录 Dir 不能为空")
	}
	st, err := newStore(opts.Dir)
	if err != nil {
		return nil, err
	}

	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	m := &Manager{
		opts:    opts,
		log:     log,
		now:     nowFn,
		store:   st,
		entries: make(map[string]*entry),
		wanted:  make(map[string]bool),
		wake:    make(chan struct{}, 1),
		updates: make(chan string, 16),
	}
	legolog.SetDefault(log.With("component", "acme"))

	loaded, skipped := st.loadCerts()
	for _, sub := range skipped {
		m.log.Warn("跳过无法加载的证书目录", slog.String("identifier", sub))
	}
	now := nowFn()
	for id, sc := range loaded {
		m.entries[id] = &entry{
			id:          id,
			cert:        sc,
			renewAt:     fallbackRenewAt(sc.leaf.NotBefore, sc.leaf.NotAfter, sc.meta.Profile),
			unusedSince: now,
		}
	}
	m.publishCerts()
	return m, nil
}

func (m *Manager) Run(ctx context.Context) {
	for {
		next := m.step(ctx)
		// 最多睡 1 分钟；下一件已经到期时只歇 1 秒再办（每件失败都会把自己的时刻往后推，不会空转）
		wait := time.Minute
		if !next.IsZero() {
			d := next.Sub(m.now())
			switch {
			case d <= 0:
				wait = time.Second
			case d < wait:
				wait = d
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopTimer(timer)
			return
		case <-m.wake:
			stopTimer(timer)
		case <-timer.C:
		}
	}
}

func stopTimer(t *time.Timer) {
	if !t.Stop() {
		select {
		case <-t.C:
		default:
		}
	}
}

func (m *Manager) Want(ids []string) {
	seen := make(map[string]bool)
	var normalized []string
	for _, raw := range ids {
		id := core.NormalizeCertIdentifier(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		normalized = append(normalized, id)
	}

	m.mu.Lock()
	m.wanted = make(map[string]bool, len(normalized))
	for _, id := range normalized {
		m.wanted[id] = true
	}

	var newWanted bool
	now := m.now()
	for _, id := range normalized {
		if _, ok := m.entries[id]; !ok {
			m.entries[id] = &entry{id: id}
			newWanted = true
		} else {
			m.entries[id].unusedSince = time.Time{}
		}
	}
	for id, e := range m.entries {
		if m.wanted[id] {
			continue
		}
		if e.unusedSince.IsZero() {
			e.unusedSince = now
		}
	}
	m.mu.Unlock()

	if newWanted {
		select {
		case m.wake <- struct{}{}:
		default:
		}
	}
}

func (m *Manager) Certificate(id string) (*tls.Certificate, bool) {
	id = core.NormalizeCertIdentifier(id)
	if id == "" {
		return nil, false
	}
	ptr := m.certs.Load()
	if ptr == nil {
		return nil, false
	}
	cert, ok := (*ptr)[id]
	return cert, ok
}

func (m *Manager) Status(id string) core.CertStatus {
	id = core.NormalizeCertIdentifier(id)
	st := core.CertStatus{Identifier: id}

	m.mu.Lock()
	e := m.entries[id]
	if e != nil {
		if e.cert != nil {
			st.NotAfter = e.cert.leaf.NotAfter
		}
		st.ErrorCode = e.errCode
		st.ErrorDetail = e.errDetail
		if !e.nextAttempt.IsZero() {
			st.NextAttempt = e.nextAttempt
		} else if e.cert != nil {
			st.NextAttempt = e.renewAt
		}
	}
	m.mu.Unlock()
	return st
}

func (m *Manager) Updates() <-chan string {
	return m.updates
}

func (m *Manager) Purge() error {
	m.mu.Lock()
	m.entries = make(map[string]*entry)
	m.wanted = make(map[string]bool)
	empty := make(map[string]*tls.Certificate)
	m.certs.Store(&empty)
	m.issuer = nil
	m.gen++
	st := m.store
	m.mu.Unlock()

	purgeErr := st.purge()
	newSt, err := newStore(m.opts.Dir)
	m.mu.Lock()
	if err == nil {
		m.store = newSt
	}
	m.mu.Unlock()
	if err != nil {
		if purgeErr != nil {
			return purgeErr
		}
		return err
	}
	return purgeErr
}

func (m *Manager) step(ctx context.Context) time.Time {
	now := m.now()

	m.mu.Lock()
	nextWake := m.nextWakeTime(now)

	var (
		pickKind int
		pickID   string
		pickAt   time.Time
	)

	for id, e := range m.entries {
		wanted := m.wanted[id]
		if wanted && m.issueDue(e, now) {
			dl := m.issueDeadline(e, now)
			if pickKind == 0 || pickKind > stepIssue || (pickKind == stepIssue && timeBefore(dl, pickAt)) {
				pickKind, pickID, pickAt = stepIssue, id, dl
			}
		}
	}
	for id, e := range m.entries {
		if m.wanted[id] {
			continue
		}
		if e.unusedSince.IsZero() {
			continue
		}
		dl := e.unusedSince.Add(unusedKeep)
		if now.Before(dl) {
			continue
		}
		if pickKind == 0 || pickKind > stepPurge || (pickKind == stepPurge && timeBefore(dl, pickAt)) {
			pickKind, pickID, pickAt = stepPurge, id, dl
		}
	}
	for id, e := range m.entries {
		if !m.wanted[id] || e.cert == nil {
			continue
		}
		if m.issueDue(e, now) {
			continue
		}
		// ariNext 零值表示还没查过（刚读盘），立即查
		if now.Before(e.ariNext) {
			continue
		}
		dl := e.ariNext
		if pickKind == 0 || pickKind > stepARI || (pickKind == stepARI && timeBefore(dl, pickAt)) {
			pickKind, pickID, pickAt = stepARI, id, dl
		}
	}

	if pickKind == 0 || (!pickAt.IsZero() && now.Before(pickAt)) {
		m.mu.Unlock()
		return nextWake
	}

	e := m.entries[pickID]
	st := m.store
	gen := m.gen
	m.mu.Unlock()

	switch pickKind {
	case stepIssue:
		m.doIssue(ctx, pickID, e, st, gen)
	case stepPurge:
		m.doRecycle(pickID, st)
	case stepARI:
		m.doARI(ctx, pickID, e, st)
	}

	m.mu.Lock()
	nextWake = m.nextWakeTime(m.now())
	m.mu.Unlock()
	return nextWake
}

func (m *Manager) issueDue(e *entry, now time.Time) bool {
	need := e.cert == nil
	if e.cert != nil {
		if !now.Before(e.renewAt) || !now.Before(e.cert.leaf.NotAfter) {
			need = true
		}
	}
	if !need {
		return false
	}
	if !e.nextAttempt.IsZero() && now.Before(e.nextAttempt) {
		return false
	}
	return true
}

func (m *Manager) issueDeadline(e *entry, now time.Time) time.Time {
	if e.cert == nil {
		if !e.nextAttempt.IsZero() && now.Before(e.nextAttempt) {
			return e.nextAttempt
		}
		return now
	}
	renew := e.renewAt
	if !now.Before(e.cert.leaf.NotAfter) && renew.After(now) {
		renew = now
	}
	if !e.nextAttempt.IsZero() && e.nextAttempt.After(renew) {
		return e.nextAttempt
	}
	return renew
}

func (m *Manager) nextWakeTime(now time.Time) time.Time {
	var next time.Time
	for id, e := range m.entries {
		if m.wanted[id] {
			if t := m.issueDeadline(e, now); !t.IsZero() && (next.IsZero() || t.Before(next)) {
				next = t
			}
			if e.cert != nil && !m.issueDue(e, now) {
				ari := e.ariNext
				if ari.IsZero() {
					ari = now
				}
				if next.IsZero() || ari.Before(next) {
					next = ari
				}
			}
		} else if !e.unusedSince.IsZero() {
			dl := e.unusedSince.Add(unusedKeep)
			if next.IsZero() || dl.Before(next) {
				next = dl
			}
		}
	}
	return next
}

func timeBefore(a, b time.Time) bool {
	if a.IsZero() {
		return false
	}
	if b.IsZero() {
		return true
	}
	return a.Before(b)
}

func (m *Manager) ensureIssuer(ctx context.Context, st *store) (*issuer, error) {
	m.mu.Lock()
	force := m.forceRegister
	if m.issuer != nil && !force {
		is := m.issuer
		m.mu.Unlock()
		return is, nil
	}
	m.mu.Unlock()

	is, err := newIssuer(ctx, st, m.opts, force)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.issuer = is
	m.forceRegister = false
	m.mu.Unlock()
	return is, nil
}

func (m *Manager) doIssue(ctx context.Context, id string, e *entry, st *store, gen uint64) {
	log := m.log.With(slog.String("identifier", id))

	is, err := m.ensureIssuer(ctx, st)
	if err != nil {
		m.applyIssueFailure(id, err, log)
		return
	}

	var previous *x509.Certificate
	if e.cert != nil {
		previous = e.cert.leaf
	}

	issueCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	certPEM, keyPEM, err := is.obtain(issueCtx, id, previous)
	cancel()

	if err != nil {
		m.applyIssueFailure(id, err, log)
		return
	}

	sc, err := st.saveCert(id, certPEM, keyPEM, certMeta{
		Identifier: id,
		Profile:    profileFor(id),
	})
	if err != nil {
		m.applyIssueFailure(id, &issueError{code: ReasonStoreFailed, err: err}, log)
		return
	}

	renewed := e.cert != nil
	now := m.now()

	m.mu.Lock()
	if m.gen != gen || m.entries[id] == nil {
		m.mu.Unlock()
		// 下单期间被 Purge 了（节点被删）：刚写的证书也不留
		if m.gen != gen {
			_ = st.remove(id)
		}
		return
	}
	e = m.entries[id]
	e.cert = sc
	e.failures = 0
	e.errCode = ""
	e.errDetail = ""
	e.nextAttempt = time.Time{}
	e.renewAt = fallbackRenewAt(sc.leaf.NotBefore, sc.leaf.NotAfter, sc.meta.Profile)
	e.ariNext = now
	m.publishCertsLocked()
	m.mu.Unlock()

	m.notifyUpdate(id)
	log.Info("证书已更新",
		slog.Time("not_after", sc.leaf.NotAfter),
		slog.Bool("renewed", renewed),
	)
}

func (m *Manager) applyIssueFailure(id string, err error, log *slog.Logger) {
	code, detail, ra := classify(err)
	now := m.now()

	m.mu.Lock()
	e := m.entries[id]
	if e != nil {
		e.failures++
		e.errCode = code
		e.errDetail = detail
		e.nextAttempt = now.Add(retryDelay(code, e.failures, ra))
	}
	var prob *acme.ProblemDetails
	if errors.As(err, &prob) && prob.Type == acme.AccountDoesNotExistErrorType {
		m.issuer = nil
		m.forceRegister = true
	}
	next := time.Time{}
	if e != nil {
		next = e.nextAttempt
	}
	m.mu.Unlock()

	log.Warn("证书申请或续期失败",
		slog.String("error_code", code),
		slog.String("detail", detail),
		slog.Time("next_attempt", next),
	)
}

func (m *Manager) doRecycle(id string, st *store) {
	if err := st.remove(id); err != nil {
		m.log.Warn("删除闲置证书失败", slog.String("identifier", id), slog.String("detail", err.Error()))
	}
	m.mu.Lock()
	delete(m.entries, id)
	m.publishCertsLocked()
	m.mu.Unlock()
}

func (m *Manager) doARI(ctx context.Context, id string, e *entry, st *store) {
	log := m.log.With(slog.String("identifier", id))
	now := m.now()

	is, err := m.ensureIssuer(ctx, st)
	if err != nil {
		m.mu.Lock()
		if ent := m.entries[id]; ent != nil {
			ent.ariNext = now.Add(ariPollInterval)
		}
		m.mu.Unlock()
		return
	}

	renewAt, pollAfter, err := is.renewalInfo(ctx, e.cert.leaf, now)
	m.mu.Lock()
	ent := m.entries[id]
	if ent == nil || ent.cert == nil {
		m.mu.Unlock()
		return
	}
	if err != nil {
		ent.ariNext = now.Add(ariPollInterval)
		m.mu.Unlock()
		log.Debug("查询 ARI 失败", slog.String("detail", err.Error()))
		return
	}
	ent.renewAt = renewAt
	ent.ariNext = now.Add(pollAfter)
	m.mu.Unlock()
}

func (m *Manager) publishCerts() {
	m.mu.Lock()
	m.publishCertsLocked()
	m.mu.Unlock()
}

func (m *Manager) publishCertsLocked() {
	out := make(map[string]*tls.Certificate, len(m.entries))
	for id, e := range m.entries {
		if e.cert != nil && e.cert.tlsCert != nil {
			out[id] = e.cert.tlsCert
		}
	}
	m.certs.Store(&out)
}

func (m *Manager) notifyUpdate(id string) {
	select {
	case m.updates <- id:
	default:
		m.log.Debug("证书更新通知通道已满，已丢弃", slog.String("identifier", id))
	}
}
