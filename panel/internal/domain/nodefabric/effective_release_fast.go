package nodefabric

import (
	"bytes"
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aegispanel/aegis/internal/platform/db"
)

// 生效发布的「没变化」快路径。
//
// 节点每个拉取周期都来要一次生效配置。绝大多数轮次里它手上那一版就是当前版：
// 原先面板照样锁节点行、读两遍发布物、把 desired_* 原值写回去（每次都是一条
// 真写入，还触发 nodes 的变更通知）、把整份 payload 重新规范化再签一次名。
//
// 现在节点带上自己已应用的 release_id + generation（AppliedEffectiveReleaseHeader），
// 面板一条无锁只读查询确认它仍是当前版，就回 204「没有新东西」：不签名、不写库、
// 不传 payload。老节点不带这个头，照旧走全量路径；老面板不认这个头，照旧回 200。

// AppliedEffectiveReleaseHeader 是节点报告「我已应用哪一版」的请求头，值为
// "<release_id>/<generation>"。它不在请求签名里：篡改它最多让面板少回一份配置，
// 等价于丢掉这次响应，而丢响应本来就要靠下一轮拉取兜底。
const AppliedEffectiveReleaseHeader = "X-Applied-Effective-Release"

// ParseAppliedEffectiveRelease 严格解析 AppliedEffectiveReleaseHeader；任何不规范的
// 写法都当作没带（走全量路径），不报错。
func ParseAppliedEffectiveRelease(value string) (releaseID string, generation uint64, ok bool) {
	id, gen, found := strings.Cut(strings.TrimSpace(value), "/")
	if !found {
		return "", 0, false
	}
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return "", 0, false
	}
	g, err := strconv.ParseUint(gen, 10, 64)
	if err != nil || g == 0 || g > math.MaxInt64 || strconv.FormatUint(g, 10) != gen {
		return "", 0, false
	}
	return id, g, true
}

// EffectiveConfigUnchanged 判断节点手上的 (releaseID, generation) 是否仍是它的当前
// 生效发布，且全量路径此刻会原样交回同一份发布物：
//   - 节点仍可下发（与 FetchEffectiveConfig 同一组条件）；
//   - 配置来源代际没动（没有待物化的新代际）；
//   - 该代际的发布物就是这一版，且由当前签名密钥签发（换了密钥要推代际，不能短路）；
//   - desired_* 已经指向它（全量路径不会再写）。
//
// 只读、不加锁。与并发的来源写入撞上时最多晚一轮：写入提交后下一次拉取（或
// 长连接推下来的 sync.config）就会看到新代际。
func (s *Service) EffectiveConfigUnchanged(ctx context.Context, tenantID, nodeID, releaseID string, generation uint64) (bool, error) {
	if generation == 0 || generation > math.MaxInt64 {
		return false, nil
	}
	var unchanged bool
	err := s.pool.InTx(ctx, db.Scope{TenantID: tenantID}, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				  FROM nodes n
				  JOIN node_effective_config_releases r
				    ON r.tenant_id=n.tenant_id AND r.node_id=n.id AND r.generation=n.config_source_generation
				 WHERE n.tenant_id=$1 AND n.id=$2::uuid
				   AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'
				   AND n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535
				   AND n.config_source_generation=$4
				   AND n.desired_effective_release_id=$3::uuid AND n.desired_effective_generation=$4
				   AND r.id=$3::uuid AND r.key_id=$5)`,
			tenantID, nodeID, releaseID, int64(generation), s.signer.KeyID()).Scan(&unchanged)
	})
	return unchanged, err
}

// reusableEffectiveRelease 是无锁读到的「当前代际已有、且是当前密钥签的」发布物。
type reusableEffectiveRelease struct {
	generation                        int64
	desiredID                         *string
	desiredGeneration                 *int64
	id, keyID                         string
	payload, content, manifest, mhash []byte
	hasRelease                        bool
}

// readReusableEffectiveReleaseTx 无锁读出节点当前代际的发布物。found=false 表示节点
// 不可下发（交给加锁路径给出原来的 404）；hasRelease=false 表示要物化或换钥。
func readReusableEffectiveReleaseTx(ctx context.Context, tx pgx.Tx, tenantID, nodeID string) (r reusableEffectiveRelease, found bool, err error) {
	var id, keyID *string
	err = tx.QueryRow(ctx, `
		SELECT n.config_source_generation,
		       n.desired_effective_release_id::text, n.desired_effective_generation,
		       r.id::text, r.key_id, r.payload, r.content_hash, r.source_manifest, r.source_manifest_hash
		  FROM nodes n
		  LEFT JOIN node_effective_config_releases r
		    ON r.tenant_id=n.tenant_id AND r.node_id=n.id AND r.generation=n.config_source_generation
		 WHERE n.tenant_id=$1 AND n.id=$2::uuid
		   AND n.status NOT IN ('destroyed','retired') AND n.serving_status<>'retired'
		   AND n.node_type IS NOT NULL AND n.server_port BETWEEN 1 AND 65535`,
		tenantID, nodeID).Scan(&r.generation, &r.desiredID, &r.desiredGeneration,
		&id, &keyID, &r.payload, &r.content, &r.manifest, &r.mhash)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	if id != nil && keyID != nil {
		r.id, r.keyID, r.hasRelease = *id, *keyID, true
	}
	return r, true, nil
}

// desiredUpToDate 是 desired_* 是否已指向这一版：是就不必再发那条 UPDATE。
func (r reusableEffectiveRelease) desiredUpToDate() bool {
	return r.desiredID != nil && *r.desiredID == r.id &&
		r.desiredGeneration != nil && *r.desiredGeneration == r.generation
}

// releaseMemoMax 是记住的发布物份数上限（每个节点通常只有当前一版在被反复拉）。
const releaseMemoMax = 4096

// releaseMemo 记住「这份原始字节 + 这个存档哈希」规范化后的结果。发布物不可变，
// 同一份字节反复取出时不必每次重做 canonicalJSON 与哈希校验；字节或哈希只要有一个
// 和记下的不同（库里被改过），就按新值重新算、重新校验，篡改照样被发现。
type releaseMemo struct {
	mu      sync.Mutex
	entries map[string]releaseMemoEntry
}

type releaseMemoEntry struct {
	raw, hash, canonical []byte
}

// canonicalVerified 返回 raw 的规范字节，hashOK 表示规范字节的 SHA-256 等于 hash。
// 只有校验通过的结果会被记住。
func (m *releaseMemo) canonicalVerified(key string, raw, hash []byte) (canonical []byte, hashOK bool, err error) {
	m.mu.Lock()
	if e, ok := m.entries[key]; ok && bytes.Equal(e.raw, raw) && bytes.Equal(e.hash, hash) {
		m.mu.Unlock()
		return e.canonical, true, nil
	}
	m.mu.Unlock()

	canonical, err = canonicalJSON(raw)
	if err != nil {
		return nil, false, err
	}
	if !hashMatches(canonical, hash) {
		return canonical, false, nil
	}
	m.remember(key, raw, hash, canonical)
	return canonical, true, nil
}

// remember 记下一份已经核对过（规范字节的哈希等于 hash）的结果。调用方负责核对。
func (m *releaseMemo) remember(key string, raw, hash, canonical []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.entries == nil || len(m.entries) >= releaseMemoMax {
		m.entries = make(map[string]releaseMemoEntry)
	}
	m.entries[key] = releaseMemoEntry{raw: append([]byte(nil), raw...),
		hash: append([]byte(nil), hash...), canonical: canonical}
}
